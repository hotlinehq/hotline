Feature: The capture ingress
  On a Mac, a small helper (a system extension, built in its own project,
  hotline-macos-capture) picks out the secure calls of the apps named in
  listen.capture.apps and hands them to the proxy over a local socket (a
  file-like channel in a private folder). Each captured connection starts
  with one framed hello message that says where the program really called
  and which program it was, followed by the program's own bytes. From there
  the proxy treats it like the intercept ingress: it reads the site name
  from the TLS handshake, shows a certificate for it signed by the
  customer's issuer, and handles each call inside like any other call, with
  hotline.ingress capture (Peter, 2026-11-10).

  The tests need no Mac: the test extension is a program that connects to
  the socket and sends the same messages.

  Background:
    Given an issuer named customer in issuers
    And listen.capture picks it for the apps curl and firefox with integration "ci-lint"
    And an app that trusts the interception issuer

  Rule: Captured calls are handled like any other call

    Scenario: A call reaches the vendor unchanged, and the record names the program
      When the extension opens a flow to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call
      Then the test vendor got the call over https with its Host, path, query, headers and body unchanged
      And the record says hotline.ingress capture and ID ci-lint
      And the record says the program is process 4242 at "/usr/bin/curl"

    Scenario: The certificate names the site in the handshake
      When the extension opens a flow to api.stripe.com:443 for process 4242 at "/usr/bin/curl" and the app names api.stripe.com in the handshake
      Then the app sees a certificate for api.stripe.com signed by the interception issuer

    Scenario: The port in the hello decides where the proxy connects
      When the extension opens a flow to api.talon.one:8443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call with the header "Host: api.talon.one:8443"
      Then the test vendor got the call over https on port 8443

    Scenario: Without an integration the ID is not-passed
      Given listen.capture picks it for the apps curl
      When the extension opens a flow to example.org:443 for process 4242 at "/usr/bin/curl" and the app names example.org in the handshake
      And the app sends its call with the header "Host: example.org"
      Then the record says hotline.ingress capture and ID not-passed

    Scenario: No extension is on the control connection
      The calls still work: the proxy only tells the extension what to pick.
      When the extension opens a flow to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call
      Then the test vendor for api.talon.one got the call

  Rule: A handshake with no site name uses the address in the hello

    Scenario: A call by IP address is intercepted with the IP as host
      When the extension opens a flow to the address of api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names no site in the handshake
      Then the app sees a certificate for the IP api.talon.one signed by the interception issuer
      When the app sends its call
      Then the proxy connected to the address of api.talon.one
      And the record says hotline.ingress capture and ID ci-lint

  Rule: Hosts in passthrough are relayed untouched

    Scenario: A listed host is relayed by its name
      Given listen.capture lists pinned.example.com in passthrough
      When the extension opens a flow to pinned.example.com:443 for process 4242 at "/usr/bin/curl" and the app names pinned.example.com in the handshake
      Then the proxy dialed pinned.example.com by its name, never the address the app chose
      And the vendor's own certificate reaches the app, not one from the interception issuer
      When the app closes its connections
      Then the capture record has hotline.ingress capture, server.address pinned.example.com, hotline.intercept.mode passthrough, hotline.intercept.reason passthrough and process.pid 4242

  Rule: Connections that cannot be intercepted are recorded with the program

    Scenario: An app that does not trust the issuer
      Given an app that does not trust the interception issuer
      When the extension opens a flow to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      Then TLS inside the tunnel fails to set up
      And nothing is sent to any vendor
      And the capture record has hotline.integration.id ci-lint, server.address api.talon.one, error.type tls_untrusted_by_app, process.pid 4242 and process.executable.path /usr/bin/curl

    Scenario: Plain text after a good hello
      When the extension opens a flow to api.talon.one:443 for process 4242 at "/usr/bin/curl" and sends plain HTTP bytes
      Then the capture record has hotline.ingress capture and error.type not_tls

  Rule: The app list goes to the extension on the control connection

    Scenario: The extension gets the list, with a skip rule for Hotline itself
      When the extension opens the control connection
      Then the extension receives the app list curl, firefox and a skip rule for Hotline's own process number

    Scenario: A reload that changes apps sends the new list
      Given the extension has opened the control connection
      And the extension receives the app list curl, firefox and a skip rule for Hotline's own process number
      When the config file changes listen.capture to the apps slack
      And the proxy checks its files
      Then the extension receives the app list slack and a skip rule for Hotline's own process number

  Rule: Messages from the extension are not trusted

    Scenario Outline: A first message that is not a usable hello
      When the extension sends <message> as its first message
      Then the capture record has hotline.ingress capture and error.type capture_bad_hello

      Examples:
        | message                                |
        | a size over 16 KiB                     |
        | a body that is not JSON                |
        | a message cut short                    |
        | a hello with an unknown version        |
        | a flow with no host                    |
        | a flow with port 0                     |
        | a flow that is not TCP                 |

  Rule: The socket

    Scenario: The socket is in a private folder, removed when the proxy stops
      Then the capture socket is in a folder with a random name, mode 0700 and owned by the current user
      When the proxy stops
      Then the capture socket folder is gone

  Rule: The config

    Scenario Outline: A listen.capture that cannot work stops the proxy at start
      Given <problem>
      When the proxy loads the config
      Then the config is refused with a message naming the problem

      Examples:
        | problem                                                    |
        | listen.capture names an issuer that is not in issuers      |
        | listen.capture names no issuer                             |
        | listen.capture lists no apps                               |
        | listen.capture lists an app that is just a skip mark       |
        | listen.capture lists an app with a space in it             |
        | listen.capture lists an ech.relay name that is not a host name |
        | listen.capture lists a passthrough pattern that is not a name or a *. suffix |
