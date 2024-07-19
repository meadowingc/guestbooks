package main

import (
	"bytes"
	"context"
	"fmt"
	"guestbook/constants"
	"log"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/spf13/viper"

	"github.com/karim-w/go-azure-communication-services/emails"
)

func SendMail(recepients []string, subject, body string) error {
	mailerToUse := viper.GetString("mailer.mailer_name")
	if mailerToUse == "azure_communication_service" {
		from := viper.GetString("mailer.azure_communication_service.from_email")
		host := viper.GetString("mailer.azure_communication_service.host")
		key := viper.GetString("mailer.azure_communication_service.key")

		client := emails.NewClient(host, key, nil)
		var err error
		for _, recipient := range recepients {
			payload := emails.Payload{
				SenderAddress: from,
				Content: emails.Content{
					Subject:   subject,
					PlainText: body,
				},
				Recipients: emails.Recipients{
					To: []emails.ReplyTo{
						{
							Address: recipient,
						},
					},
				},
			}
			_, err = client.SendEmail(context.Background(), payload)
			if err != nil {
				log.Printf("WARN: Failed to send email: %v\n", err)
			}
		}
		return err

	} else if mailerToUse == "smtp" {
		from := viper.GetString("mailer.smtp.from_email")
		host := viper.GetString("mailer.smtp.host")
		port := viper.GetString("mailer.smtp.port")
		username := viper.GetString("mailer.smtp.username")
		password := viper.GetString("mailer.smtp.password")

		auth := sasl.NewLoginClient(username, password)

		var err error
		for _, recipient := range recepients {
			message := "From: " + from + "\n" +
				"To: " + recipient + "\n" +
				"Subject: " + subject + "\n\n" +
				body

			to := []string{recipient}
			msg := []byte(message)
			reader := bytes.NewReader(msg)
			err = smtp.SendMail(host+":"+port, auth, from, to, reader)
			if err != nil {
				log.Printf("WARN: Failed to send email: %v\n", err)
			}
		}

		return err
	} else {
		log.Fatalf("Unknown mailer config: %s\n", mailerToUse)
		panic("unknown mailer config")
	}
}

func SendVerificationEmail(recipient, token string) error {
	subject := "[Guestbooks] Please verify your email address"
	verificationLink := fmt.Sprintf(constants.PUBLIC_URL+"/verify-email?token=%s", token)
	body := fmt.Sprintf("Please click on the following link to verify your email address: %s", verificationLink)

	return SendMail([]string{recipient}, subject, body)
}
