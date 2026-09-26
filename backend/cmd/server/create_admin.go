package main

import (
	"context"
	"fmt"
	"os"

	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
	"golang.org/x/term"
)

func createAdmin(databasePath, username string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open terminal for password input: %w", err)
	}
	defer tty.Close()
	if !term.IsTerminal(int(tty.Fd())) {
		return fmt.Errorf("interactive terminal required for administrator password")
	}
	readPassword := func(prompt string) (string, error) {
		if _, err := fmt.Fprint(tty, prompt); err != nil {
			return "", err
		}
		value, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		if err != nil {
			return "", err
		}
		return string(value), nil
	}
	password, err := readPassword("Administrator password: ")
	if err != nil {
		return err
	}
	confirmation, err := readPassword("Confirm password: ")
	if err != nil {
		return err
	}
	if password != confirmation {
		return fmt.Errorf("passwords do not match")
	}
	db, err := store.Open(context.Background(), databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	id, err := adminauth.New(db, nil).CreateAdmin(context.Background(), username, password)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(tty, "Administrator created (ID %d).\n", id)
	return err
}
