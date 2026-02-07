# Gmail Notifications

A lightweight Go service that monitors your Gmail inbox via IMAP and sends native Ubuntu desktop notifications for new emails. Displays sender, subject, and a snippet of the email body directly in your system tray. Runs as a background daemon, checking for new messages every 15 seconds.

## Usage

```bash
export GMAIL_USER="your-email@gmail.com"
export GMAIL_NOTIFICATIONS="your-app-password"
./gmail-reader
```

## Arguments

| Flag | Description |
|------|-------------|
| `-l`, `--length` | Max body length for notifications (default: 500, 0=disables body) |
| `-r`, `--read` | Read last x emails to stdout and exit |
| `-h`, `--help` | Show help message |
