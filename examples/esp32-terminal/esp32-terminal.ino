// Protocol example for Arduino ESP32. Enter one pmt_ token per serial line.
// Configure these values before uploading; never print TERMINAL_KEY.
#include <Arduino.h>
#include <WiFi.h>
#include <NetworkClient.h>
#include <NetworkClientSecure.h>
#include <HTTPClient.h>

const char *WIFI_SSID = "replace-wifi-name";
const char *WIFI_PASSWORD = "replace-wifi-password";
const char *SERVER_URL = "http://192.168.1.10:5001";
const char *TERMINAL_KEY = "trm_replace-with-complete-terminal-key";
// For HTTPS, replace with the issuing root CA certificate in PEM format.
const char *ROOT_CA = "";

void scanToken(String token) {
  token.trim();
  // Current tokens are pmt_ plus 43 base64url characters; validation also
  // makes inserting the token into the JSON string unambiguous.
  if (token.length() != 47 || !token.startsWith("pmt_")) {
    Serial.println("Invalid token format");
    return;
  }
  for (size_t i = 4; i < token.length(); ++i) {
    const char ch = token[i];
    if (!((ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
          (ch >= '0' && ch <= '9') || ch == '-' || ch == '_')) {
      Serial.println("Invalid token format");
      return;
    }
  }
  String base(SERVER_URL);
  while (base.endsWith("/")) base.remove(base.length() - 1);
  NetworkClient plain;
  NetworkClientSecure secure;
  HTTPClient http;
  bool ready;
  if (base.startsWith("https://")) {
    if (ROOT_CA[0] == '\0') {
      Serial.println("Configure ROOT_CA for HTTPS");
      return;
    }
    secure.setCACert(ROOT_CA);
    ready = http.begin(secure, base + "/api/v1/terminal/scan");
  } else {
    ready = http.begin(plain, base + "/api/v1/terminal/scan");
  }
  if (!ready) { Serial.println("Cannot initialize request"); return; }
  http.setConnectTimeout(4000);
  http.setTimeout(4000);
  http.addHeader("Content-Type", "application/json");
  http.addHeader("Authorization", String("Bearer ") + TERMINAL_KEY);
  const int status = http.POST(String("{\"token\":\"") + token + "\"}");
  if (status > 0) {
    Serial.printf("HTTP %d\n", status);
    Serial.println(http.getString());
    // Parse the JSON before displaying any success indication:
    // SUCCESS = completed; FAILED = rejected; PENDING = employee confirmation.
    // Do not announce a new payment for replayed/duplicate_trigger results.
  } else {
    Serial.println("Request failed; payment outcome is unknown");
  }
  http.end();
}

void setup() {
  Serial.begin(115200);
  Serial.setTimeout(1000);
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);
  const unsigned long start = millis();
  while (WiFi.status() != WL_CONNECTED && millis() - start < 15000) delay(100);
  Serial.println(WiFi.status() == WL_CONNECTED ? "Ready: enter one token per line" : "Wi-Fi unavailable");
}

void loop() {
  if (WiFi.status() == WL_CONNECTED && Serial.available()) scanToken(Serial.readStringUntil('\n'));
  delay(10);
}
