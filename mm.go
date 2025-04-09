package main

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"flag"
	"fmt"
	"net/smtp"
	"os"
	"sort"
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
	SMTPHost string `yaml:"smtp_host"`
	SMTPPort string `yaml:"smtp_port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Only 8 core NNTP headers get special ordering
var headerPriority = map[string]int{
	"from":         1,
	"reply-to":     2,
	"newsgroups":   3,
	"subject":      4,
	"date":         5,
	"message-id":   6,
	"mime-version": 7,
	"content-transfer-encoding": 8,
	"content-type": 9,  
	"references":   10,
	"organization": 11,
	// All other headers sorted alphabetically
}

func generateRandomLetters(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return "xxxxx"
	}
	for i := range b {
		b[i] = letters[b[i]%26]
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
	line = strings.TrimPrefix(strings.TrimPrefix(line, "To:"), "From:")
	line = strings.TrimSpace(line)

	if idx := strings.LastIndex(line, "<"); idx != -1 {
		return EmailAddress{
			Name:    strings.TrimSpace(line[:idx]),
			Address: strings.Trim(line[idx:], "<> "),
		}
	}
	if idx := strings.Index(line, "("); idx != -1 {
		return EmailAddress{
			Address: strings.TrimSpace(line[:idx]),
			Name:    strings.Trim(line[idx:], "() "),
		}
	}
	return EmailAddress{Address: line}
}

func loadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var config Config
	err = yaml.Unmarshal(data, &config)
	return &config, err
}

func sortHeaders(headers map[string]string) string {
	var priorityHeaders, otherHeaders []string

	for key := range headers {
		if prio, exists := headerPriority[strings.ToLower(key)]; exists {
			priorityHeaders = append(priorityHeaders, fmt.Sprintf("%d|%s", prio, key))
		} else {
			otherHeaders = append(otherHeaders, key)
		}
	}

	// Sort priority headers by their priority number
	sort.Strings(priorityHeaders)
	// Sort other headers alphabetically
	sort.Strings(otherHeaders)

	var result strings.Builder
	
	// Write priority headers first
	for _, h := range priorityHeaders {
		parts := strings.SplitN(h, "|", 2)
		key := parts[1]
		result.WriteString(fmt.Sprintf("%s: %s\r\n", key, headers[key]))
	}
	
	// Then all other headers
	for _, key := range otherHeaders {
		result.WriteString(fmt.Sprintf("%s: %s\r\n", key, headers[key]))
	}

	return result.String()
}

func main() {
	debug := flag.Bool("d", false, "Enable debug output")
	username := flag.String("u", "", "SMTP username (optional)")
	password := flag.String("p", "", "SMTP password (optional)")
	configFile := flag.String("c", "", "Path to config file (optional)")
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
			fmt.Println("Usage: mm [-d] [-u username] [-p password] [-c configfile] [smtp-server port] < message.txt")
			os.Exit(1)
		}
		host = args[0]
		port = args[1]
	}

	if *debug {
		fmt.Printf("Connecting to %s:%s via SOCKS5 proxy\n", host, port)
	}

	scanner := bufio.NewScanner(os.Stdin)
	headers := make(map[string]string)
	var body strings.Builder
	var hasFrom bool
	fromEmail := EmailAddress{Name: "Mini Mailer", Address: "bounce.me@mini.mailer.msg"}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			headers[key] = value
			
			if strings.EqualFold(key, "From") {
				fromEmail = parseEmailHeader(line)
				hasFrom = true
			}
		}
	}

	for scanner.Scan() {
		body.WriteString(scanner.Text() + "\r\n")
	}

	if !hasFrom {
		headers["From"] = fmt.Sprintf("%s <%s>", fromEmail.Name, fromEmail.Address)
	}

	if _, exists := headers["Message-ID"]; !exists {
		headers["Message-ID"] = generateMessageID()
	}

	headers["User-Agent"] = "Mini Mailer v0.1.2"

	sortedHeaders := sortHeaders(headers)
	message := sortedHeaders + "\r\n" + body.String()

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

	toEmail := parseEmailHeader(headers["To"])
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

	if *debug {
		fmt.Println("SMTP: DATA")
		fmt.Println("Headers being sent:")
		fmt.Print(sortedHeaders)
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