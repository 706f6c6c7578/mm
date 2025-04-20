package main

import (
    "bufio"
    "crypto/rand"
    "crypto/tls"
    "flag"
    "fmt"
    "net/smtp"
    "os"
    "strings"
    "time"
    "golang.org/x/net/proxy"
    "gopkg.in/yaml.v2"
)

type EmailAddress struct {
    Name    string
    Address string
}

type Config struct {
    SMTPHost  string `yaml:"smtp_host"`
    SMTPPort  string `yaml:"smtp_port"`
    Username  string `yaml:"username"`
    Password  string `yaml:"password"`
    SocksPort string `yaml:"socks_port"`
}

func generateRandomLetters(n int) string {
    const letters = "abcdefghijklmnopqrstuvwxyz"
    b := make([]byte, n)
    randomBytes := make([]byte, n)
    _, err := rand.Read(randomBytes)
    if err != nil {
        return "xxxxx" // fallback in case of error
    }
    for i := range b {
        b[i] = letters[randomBytes[i]%26]
    }
    return string(b)
}

func generateMessageID() string {
    randomBytes := make([]byte, 5)
    rand.Read(randomBytes)
    timestamp := time.Now().Unix()
    domain := generateRandomLetters(5)
    tld := generateRandomLetters(2)
    return fmt.Sprintf("<%x.%d@%s.%s>", randomBytes, timestamp, domain, tld)
}

func parseEmailHeader(line string) EmailAddress {
    if strings.HasPrefix(line, "To:") {
        line = strings.TrimPrefix(line, "To:")
    }
    if strings.HasPrefix(line, "From:") {
        line = strings.TrimPrefix(line, "From:")
    }
    line = strings.TrimSpace(line)

    if idx := strings.LastIndex(line, "<"); idx != -1 {
        name := strings.TrimSpace(line[:idx])
        email := strings.TrimSpace(strings.Trim(line[idx:], "<>"))
        return EmailAddress{Name: name, Address: email}
    }

    if idx := strings.Index(line, "("); idx != -1 {
        email := strings.TrimSpace(line[:idx])
        name := strings.TrimSpace(strings.Trim(line[idx:], "()"))
        return EmailAddress{Name: name, Address: email}
    }

    return EmailAddress{Address: line}
}

func extractToHeader(headers string) string {
    lines := strings.Split(headers, "\r\n")
    for _, line := range lines {
        if strings.HasPrefix(line, "To:") {
            return strings.TrimSpace(strings.TrimPrefix(line, "To:"))
        }
    }
    return ""
}

func extractDateHeader(headers string) string {
    lines := strings.Split(headers, "\r\n")
    for _, line := range lines {
        if strings.HasPrefix(line, "Date:") {
            return strings.TrimSpace(strings.TrimPrefix(line, "Date:"))
        }
    }
    return ""
}

func extractMessageIDHeader(headers string) string {
    lines := strings.Split(headers, "\r\n")
    for _, line := range lines {
        if strings.HasPrefix(line, "Message-ID:") {
            return strings.TrimSpace(strings.TrimPrefix(line, "Message-ID:"))
        }
    }
    return ""
}

func loadConfig(filename string) (*Config, error) {
    data, err := os.ReadFile(filename)
    if err != nil {
        return nil, err
    }

    var config Config
    err = yaml.Unmarshal(data, &config)
    if err != nil {
        return nil, err
    }

    return &config, nil
}

func main() {
    debug := flag.Bool("d", false, "Enable debug output")
    username := flag.String("u", "", "SMTP username (optional)")
    password := flag.String("p", "", "SMTP password (optional)")
    configFile := flag.String("c", "", "Path to config file (optional)")
    socksPort := flag.String("s", "", "SOCKS5 proxy port (default: 9050)")
    omitUserAgent := flag.Bool("o", false, "Omit the User-Agent header")
    flag.Parse()

    var host, port string
    var config *Config

    if *configFile != "" {
        var err error
        config, err = loadConfig(*configFile)
        if err != nil {
            fmt.Println("Error loading config file:", err)
            os.Exit(1)
        }
        host = config.SMTPHost
        port = config.SMTPPort
        if *username == "" {
            *username = config.Username
        }
        if *password == "" {
            *password = config.Password
        }
    } else {
        args := flag.Args()
        if len(args) != 2 {
            fmt.Println("Usage: mm [-d] [-u username] [-p password] [-c configfile] [-s socks_port] \n          [-o user_agent] [smtp-server port] < message_with_headers.txt")
            os.Exit(1)
        }
        host = args[0]
        port = args[1]
    }

    // Determine SOCKS port (priority: command line > configuration > default value)
    finalSocksPort := "9050"
    if *socksPort != "" {
        finalSocksPort = *socksPort
    } else if config != nil && config.SocksPort != "" {
        finalSocksPort = config.SocksPort
    }

    scanner := bufio.NewScanner(os.Stdin)
    var headers, body string
    var fromEmail EmailAddress

    fromEmail = EmailAddress{Name: "Mini Mailer", Address: "bounce.me@mini.mailer.msg"}
    hasFromHeader := false

    for scanner.Scan() {
        line := scanner.Text()
        if line == "" {
            break
        }
        headers += line + "\r\n"

        if strings.HasPrefix(line, "From:") {
            fromEmail = parseEmailHeader(line)
            hasFromHeader = true
        }
    }

    if !hasFromHeader {
        headers = fmt.Sprintf("From: %s <%s>\r\n", fromEmail.Name, fromEmail.Address) + headers
    }

    for scanner.Scan() {
        body += scanner.Text() + "\r\n"
    }

    if headers == "" {
        fmt.Println("Error: No headers found in the input")
        os.Exit(1)
    }

    toHeader := extractToHeader(headers)
    if toHeader == "" {
        fmt.Println("Error: No To: header found in the input")
        os.Exit(1)
    }
    toEmail := parseEmailHeader(toHeader)

    // Extract Date header from input
    dateHeader := extractDateHeader(headers)
    if dateHeader != "" {
        // Remove the extracted Date header from the original headers to avoid duplication
        headers = strings.Replace(headers, "Date: "+dateHeader+"\r\n", "", 1)
    }

    // Extract Message-ID header from input
    messageID := extractMessageIDHeader(headers)
    if messageID != "" {
        // Remove the extracted Message-ID header from the original headers to avoid duplication
        headers = strings.Replace(headers, "Message-ID: "+messageID+"\r\n", "", 1)
    } else {
        // Generate a new Message-ID if none is provided
        messageID = generateMessageID()
    }

    if *debug {
        fmt.Printf("Connecting to %s:%s via SOCKS5 proxy (port %s)\n", host, port, finalSocksPort)
    }

    dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:"+finalSocksPort, nil, proxy.Direct)
    if err != nil {
        fmt.Println("Error creating proxy dialer:", err)
        os.Exit(1)
    }

    conn, err := dialer.Dial("tcp", host+":"+port)
    if err != nil {
        fmt.Println("Error connecting:", err)
        os.Exit(1)
    }

    if *debug {
        fmt.Println("Connected, establishing SMTP session")
    }

    c, err := smtp.NewClient(conn, host)
    if err != nil {
        fmt.Println("Error creating SMTP client:", err)
        os.Exit(1)
    }

    if *debug {
        fmt.Println("Starting TLS")
    }
    _ = c.StartTLS(&tls.Config{InsecureSkipVerify: true, ServerName: host})

    if *username != "" && *password != "" {
        if *debug {
            fmt.Println("SMTP: AUTH LOGIN")
        }
        auth := smtp.PlainAuth("", *username, *password, host)
        if err := c.Auth(auth); err != nil {
            fmt.Println("Error authenticating:", err)
            os.Exit(1)
        }
    }

    if *debug {
        fmt.Printf("SMTP: MAIL FROM:%s\n", fromEmail.Address)
    }
    if err = c.Mail(fromEmail.Address); err != nil {
        fmt.Println("Error MAIL FROM:", err)
        os.Exit(1)
    }

    if *debug {
        fmt.Printf("SMTP: RCPT TO:%s\n", toEmail.Address)
    }
    if err = c.Rcpt(toEmail.Address); err != nil {
        fmt.Println("Error RCPT TO:", err)
        os.Exit(1)
    }

    w, err := c.Data()
    if err != nil {
        fmt.Println("Error getting data writer:", err)
        os.Exit(1)
    }

    // Add the Message-ID header
    headers += fmt.Sprintf("Message-ID: %s\r\n", messageID)

    // Add the Date header only if it was provided in the input
    if dateHeader != "" {
        headers += fmt.Sprintf("Date: %s\r\n", dateHeader)
    }

    // Add the User-Agent header unless omitted
    if !*omitUserAgent {
        headers += fmt.Sprintf("User-Agent: Mini Mailer v0.1.2\r\n")
    }

    message := headers + "\r\n" + body

    if *debug {
        fmt.Println("SMTP: DATA")
        fmt.Println("Headers being sent:")
        fmt.Print(headers)
        fmt.Println("--- Message body omitted ---")
    }

    _, err = w.Write([]byte(message))
    if err != nil {
        fmt.Println("Error writing message:", err)
        os.Exit(1)
    }

    err = w.Close()
    if err != nil {
        fmt.Println("Error closing writer:", err)
        os.Exit(1)
    }

    if *debug {
        fmt.Println("SMTP: QUIT")
    }
    c.Quit()
    fmt.Println("Message sent successfully!")
}