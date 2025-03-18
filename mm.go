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
)

type EmailAddress struct {
    Name    string
    Address string
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

    // Check for format: "Name <email>"
    if idx := strings.LastIndex(line, "<"); idx != -1 {
        name := strings.TrimSpace(line[:idx])
        email := strings.TrimSpace(strings.Trim(line[idx:], "<>"))
        return EmailAddress{Name: name, Address: email}
    }

    // Check for format: "email (Name)"
    if idx := strings.Index(line, "("); idx != -1 {
        email := strings.TrimSpace(line[:idx])
        name := strings.TrimSpace(strings.Trim(line[idx:], "()"))
        return EmailAddress{Name: name, Address: email}
    }

    // Just email
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

func main() {
    debug := flag.Bool("d", false, "Enable debug output")
    username := flag.String("u", "", "SMTP username (optional)")
    password := flag.String("p", "", "SMTP password (optional)")
    flag.Parse()

    args := flag.Args()
    if len(args) != 2 {
        fmt.Println("Usage: mm [-d] [-u username] [-p password] smtp-server port < message_with_headers.txt")
        os.Exit(1)
    }

    host := args[0]
    port := args[1]

    scanner := bufio.NewScanner(os.Stdin)
    var headers, body string
    var fromEmail EmailAddress

    // Default From: header
    fromEmail = EmailAddress{Name: "Mini Mailer", Address: "noreply@mini.mailer.msg"}
    hasFromHeader := false

    // Read headers until an empty line is found
    for scanner.Scan() {
        line := scanner.Text()
        if line == "" {
            break // Empty line indicates the end of headers
        }
        headers += line + "\r\n"

        // Check if the line is a From: header
        if strings.HasPrefix(line, "From:") {
            fromEmail = parseEmailHeader(line)
            hasFromHeader = true
        }
    }

    // If no From: header was found, add the default one
    if !hasFromHeader {
        headers = fmt.Sprintf("From: %s <%s>\r\n", fromEmail.Name, fromEmail.Address) + headers
    }

    // Read the rest as the message body
    for scanner.Scan() {
        body += scanner.Text() + "\r\n"
    }

    // If no headers were found, exit with an error
    if headers == "" {
        fmt.Println("Error: No headers found in the input")
        os.Exit(1)
    }

    // Extract the To: header
    toHeader := extractToHeader(headers)
    if toHeader == "" {
        fmt.Println("Error: No To: header found in the input")
        os.Exit(1)
    }
    toEmail := parseEmailHeader(toHeader)

    if *debug {
        fmt.Printf("Connecting to %s:%s via SOCKS5 proxy\n", host, port)
    }

    dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:9050", nil, proxy.Direct)
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

    messageID := generateMessageID()

    // Add the Message-ID header
    headers += fmt.Sprintf("Message-ID: %s\r\n", messageID)

    // Build the final message
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