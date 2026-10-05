package main

import (
	"errors"
	"os"

	"github.com/joho/godotenv"
)

func loadDotEnv() error {
	if err := godotenv.Load(".env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Parser errors can include file contents, including credentials.
		return errors.New("could not load .env: check file readability and dotenv syntax")
	}
	return nil
}
