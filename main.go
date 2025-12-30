package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
	"github.com/esiqveland/notify"
	"github.com/godbus/dbus/v5"
)

var (
	user          string
	pass          string
	bodyMaxLength int
	readLast      int
	showHelp      bool
)

const uidFile = ".gmail_last_uid.txt"

func usage() {
	fmt.Printf(`Gmail Desktop Notifier - Monitors Gmail and sends desktop notifications

Usage: %s [OPTIONS]

Environment Variables (required):
  GMAIL_USER               Gmail address
  GMAIL_READER             Gmail app password

Options:
  -b, --body <int>         Max body length for notifications (default: 1000, 0=disables body)
  -r, --read <int>         Read last x emails to stdout and exit
  -h, --help               Show this help message
`, os.Args[0])
}

func main() {
	flag.IntVar(&bodyMaxLength, "b", 1000, "")
	flag.IntVar(&bodyMaxLength, "body", 1000, "")
	flag.IntVar(&readLast, "r", 0, "")
	flag.IntVar(&readLast, "read", 0, "")
	flag.BoolVar(&showHelp, "h", false, "")
	flag.BoolVar(&showHelp, "help", false, "")
	flag.Usage = usage
	flag.Parse()

	if showHelp {
		usage()
		return
	}

	if user = os.Getenv("GMAIL_USER"); user == "" {
		fmt.Println("Error: GMAIL_USER (gmail address) environment variable must be set")
		os.Exit(1)
	}
	if pass = os.Getenv("GMAIL_READER"); pass == "" {
		fmt.Println("Error: GMAIL_READER (app password) environment variable must be set")
		os.Exit(1)
	}

	// Read last x emails and exit
	if readLast > 0 {
		readEmails(user, pass, readLast)
		return
	}

	lastUID := loadUID()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	checkMail(user, pass, &lastUID)

	for {
		select {
		case <-ticker.C:
			checkMail(user, pass, &lastUID)
		case <-sigChan:
			return
		}
	}
}

func saveUID(uid uint32) {
	os.WriteFile(uidFile, []byte(strconv.FormatUint(uint64(uid), 10)), 0644)
}

func loadUID() uint32 {
	data, err := os.ReadFile(uidFile)
	if err != nil {
		return 0
	}
	uid, _ := strconv.ParseUint(string(data), 10, 32)
	return uint32(uid)
}

func sendNotification(sender, subject, body string) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return
	}
	notifier, _ := notify.New(conn)

	_, _ = notifier.SendNotification(notify.Notification{
		AppName:       "Gmail",
		Summary:       fmt.Sprintf("From: %s", sender),
		Body:          fmt.Sprintf("<b>%s</b>\n\n%s", subject, body),
		ExpireTimeout: 10000, // 10 seconds
	})
}

func checkMail(user, pass string, lastUID *uint32) {
	c, err := client.DialTLS("imap.gmail.com:993", nil)
	if err != nil {
		return
	}
	defer c.Logout()

	if err := c.Login(user, pass); err != nil {
		return
	}

	mbox, _ := c.Select("INBOX", false)
	if mbox.Messages == 0 {
		return
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(mbox.Messages)

	// Fetch Envelope, UID, and optionally Body
	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid}
	if bodyMaxLength > 0 {
		items = append(items, section.FetchItem())
	}

	messages := make(chan *imap.Message, 1)
	go func() {
		c.Fetch(seqset, items, messages)
	}()

	if msg, ok := <-messages; ok {
		if *lastUID != 0 && msg.Uid > *lastUID {
			sender := msg.Envelope.From[0].Address()
			subject := msg.Envelope.Subject
			date := msg.Envelope.Date.Format("2006-01-02 15:04")

			// Parse Body if enabled
			bodyText := ""
			if bodyMaxLength > 0 {
				if r := msg.GetBody(section); r != nil {
					mr, err := mail.CreateReader(r)
					if err == nil {
						for {
							p, err := mr.NextPart()
							if err == io.EOF {
								break
							}
							if err != nil {
								break
							}
							switch h := p.Header.(type) {
							case *mail.InlineHeader:
								contentType, _, _ := h.ContentType()
								if contentType == "text/plain" {
									b, _ := io.ReadAll(p.Body)
									bodyText = string(b)
								}
							}
						}
					}
				}
			}

			// Truncate for display
			displayBody := bodyText
			if bodyMaxLength > 0 && len(displayBody) > bodyMaxLength {
				displayBody = displayBody[:bodyMaxLength-3] + "..."
			}

			fmt.Printf("─────────────────────────────────────────\n")
			fmt.Printf("From: %s\nDate: %s\nSubject: %s\n\n%s\n", sender, date, subject, displayBody)
			sendNotification(sender, subject, displayBody)
		}
		*lastUID = msg.Uid
		saveUID(msg.Uid)
	}
}

func readEmails(user, pass string, count int) {
	c, err := client.DialTLS("imap.gmail.com:993", nil)
	if err != nil {
		fmt.Println("Connection error:", err)
		return
	}
	defer c.Logout()

	if err := c.Login(user, pass); err != nil {
		fmt.Println("Login error:", err)
		return
	}

	mbox, _ := c.Select("INBOX", false)
	if mbox.Messages == 0 {
		fmt.Println("No messages")
		return
	}

	// Calculate range for last x emails
	from := uint32(1)
	if mbox.Messages > uint32(count) {
		from = mbox.Messages - uint32(count) + 1
	}

	seqset := new(imap.SeqSet)
	seqset.AddRange(from, mbox.Messages)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid}
	if bodyMaxLength > 0 {
		items = append(items, section.FetchItem())
	}

	messages := make(chan *imap.Message, count)
	go func() {
		c.Fetch(seqset, items, messages)
	}()

	for msg := range messages {
		sender := msg.Envelope.From[0].Address()
		subject := msg.Envelope.Subject
		date := msg.Envelope.Date.Format("2006-01-02 15:04")

		// Parse Body if enabled
		bodyText := ""
		if bodyMaxLength > 0 {
			if r := msg.GetBody(section); r != nil {
				mr, err := mail.CreateReader(r)
				if err == nil {
					for {
						p, err := mr.NextPart()
						if err == io.EOF {
							break
						}
						if err != nil {
							break
						}
						switch h := p.Header.(type) {
						case *mail.InlineHeader:
							contentType, _, _ := h.ContentType()
							if contentType == "text/plain" {
								b, _ := io.ReadAll(p.Body)
								bodyText = string(b)
							}
						}
					}
				}
			}

			if len(bodyText) > bodyMaxLength {
				bodyText = bodyText[:bodyMaxLength-3] + "..."
			}
		}

		fmt.Printf("─────────────────────────────────────────\n")
		fmt.Printf("From: %s\nDate: %s\nSubject: %s\n\n%s\n", sender, date, subject, bodyText)
		sendNotification(sender, subject, bodyText)
	}
}
