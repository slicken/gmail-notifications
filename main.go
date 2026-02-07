package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
	"github.com/esiqveland/notify"
	"github.com/godbus/dbus/v5"
)

var urlRegex = regexp.MustCompile(`https?://[^\s<>"]+`)

var (
	user      string
	pass      string
	msgLenght int
	readLast  int
	showHelp  bool
)

const uidFile = ".gmail_last_uid.txt"

func usage() {
	fmt.Printf(`Gmail Desktop Notifier - Monitors Gmail and sends desktop notifications

Usage: %s [OPTIONS]

Environment Variables (required):
  GMAIL_USER               Gmail address
  GMAIL_READER             Gmail app password

Options:
  -l, --length <int>       Message body length for notifications (default: 500, 0=disable)
  -r, --read <int>         Read last x emails to stdout and exit
  -h, --help               Show this help message
`, os.Args[0])
}

func main() {
	flag.IntVar(&msgLenght, "l", 500, "")
	flag.IntVar(&msgLenght, "length", 500, "")
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
		readEmails(user, pass, readLast, nil)
		return
	}

	lastUID := loadUID()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	readEmails(user, pass, 1, &lastUID)

	for {
		select {
		case <-ticker.C:
			readEmails(user, pass, 1, &lastUID)
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

// truncateBody truncates text without cutting URLs
// If cutting would split a URL, cuts before the URL instead
func truncateBody(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}

	// Find all URLs and their positions
	urls := urlRegex.FindAllStringIndex(text, -1)

	// Find safe cut point
	cutPoint := maxLen - 3 // leave room for "..."

	for _, url := range urls {
		urlStart, urlEnd := url[0], url[1]

		// If cut point is inside a URL, move it before the URL
		if cutPoint > urlStart && cutPoint < urlEnd {
			cutPoint = urlStart
			break
		}
	}

	// If cut point is 0 or negative (URL at start is too long), skip body
	if cutPoint <= 0 {
		return ""
	}

	return text[:cutPoint] + "..."
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

// readEmails fetches emails from Gmail
// count: number of emails to fetch
// lastUID: if not nil, only process emails newer than this UID and update it
func readEmails(user, pass string, count int, lastUID *uint32) {
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

	// Calculate range for last x emails
	from := mbox.Messages
	if uint32(count) < mbox.Messages {
		from = mbox.Messages - uint32(count) + 1
	}

	seqset := new(imap.SeqSet)
	seqset.AddRange(from, mbox.Messages)

	// Fetch Envelope, UID, and optionally Body (Peek=true to not mark as read)
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid}
	if msgLenght > 0 {
		items = append(items, section.FetchItem())
	}

	messages := make(chan *imap.Message, count)
	go func() {
		c.Fetch(seqset, items, messages)
	}()

	for msg := range messages {
		// In daemon mode, skip already seen emails and update UID
		if lastUID != nil {
			if *lastUID != 0 && msg.Uid <= *lastUID {
				continue
			}
			*lastUID = msg.Uid
			saveUID(msg.Uid)
		}

		sender := msg.Envelope.From[0].Address()
		subject := msg.Envelope.Subject
		date := msg.Envelope.Date.Format("2006-01-02 15:04")

		// Parse Body if enabled
		bodyText := ""
		if msgLenght > 0 {
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

			bodyText = truncateBody(bodyText, msgLenght)
		}

		fmt.Printf("─────────────────────────────────────────\n")
		fmt.Printf("From: %s\nDate: %s\nSubject: %s\n\n%s\n", sender, date, subject, bodyText)
		sendNotification(sender, subject, bodyText)
	}
}
