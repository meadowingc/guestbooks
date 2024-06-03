package main

import (
	"bytes"
	"log"

	"github.com/spf13/viper"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

func SendMail(recepients []string, subject, body string) error {
	from := viper.GetString("smtp.from_email")
	host := viper.GetString("smtp.host")
	port := viper.GetString("smtp.port")
	username := viper.GetString("smtp.username")
	password := viper.GetString("smtp.password")

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
}
