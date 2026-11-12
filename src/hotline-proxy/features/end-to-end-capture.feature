@e2e
Feature: The capture socket, end to end
  The capture extension sends its CONNECT to listen.capture, a socket file,
  and the real hotline-proxy program asks the system which user runs the
  program on the other end. Only root, as the extension runs, is believed
  (Peter, 2026-11-12). This test is that other program, so the answer
  depends on the user the test runs as.

  Background:
    Given a fake OTLP receiver

  Scenario: The program asks the system which user opened the socket
    Given the hotline-proxy program runs with listen.connect and listen.capture and integration test-shop for the test vendor
    When this test sends a CONNECT for the test vendor with the capture headers on the capture socket
    Then the program takes the CONNECT when this test runs as root, and refuses it with 403 otherwise
    And the program gets SIGTERM
    And it exits with code 0 within 10 seconds
