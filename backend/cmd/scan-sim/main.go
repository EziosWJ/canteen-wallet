package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:8081", "internal server URL")
	credentialFile := flag.String("credential-file", "", "terminal credential file")
	repeat := flag.Int("repeat", 1, "number of times to submit the same token")
	confirm := flag.Bool("confirm", false, "confirm each pending consumption")
	offline := flag.Bool("offline", false, "simulate backend unavailability")
	flag.Parse()
	if *credentialFile == "" || *repeat < 1 || *repeat > 100 {
		fmt.Fprintln(os.Stderr, "credential-file and repeat 1..100 required")
		os.Exit(2)
	}
	credential, err := os.ReadFile(*credentialFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprint(os.Stderr, "Paste a real payment token and press Enter: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	token := strings.TrimSpace(line)
	if token == "" {
		fmt.Fprintln(os.Stderr, "token required")
		os.Exit(2)
	}
	url := strings.TrimRight(*endpoint, "/")
	if *offline {
		url = "http://127.0.0.1:1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	for i := 0; i < *repeat; i++ {
		response, err := submit(client, url, "scan", strings.TrimSpace(string(credential)), map[string]string{"token": token})
		if err != nil {
			fmt.Fprintln(os.Stderr, "backend unavailable; scan rejected and discarded:", err)
			continue
		}
		fmt.Printf("submission %d: %s\n", i+1, response)
		if *confirm {
			var result struct {
				PendingID string `json:"pending_id"`
			}
			if json.Unmarshal(response, &result) == nil && result.PendingID != "" {
				confirmation, err := submit(client, url, "confirm", strings.TrimSpace(string(credential)), map[string]string{"pending_id": result.PendingID})
				if err != nil {
					fmt.Fprintln(os.Stderr, "confirmation rejected:", err)
				} else {
					fmt.Printf("confirmation: %s\n", confirmation)
				}
			}
		}
	}
}

func submit(client *http.Client, endpoint, action, credential string, payload any) ([]byte, error) {
	body, _ := json.Marshal(payload)
	request, err := http.NewRequest(http.MethodPost, endpoint+"/api/v1/terminal/"+action, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", response.StatusCode, data)
	}
	return data, nil
}
