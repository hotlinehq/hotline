Feature: The capture ingress
  On a Mac, a small helper (a system extension, built in its own project,
  hotline-macos-capture) picks out the connections of the apps named in the
  --capture flag and hands each one to the proxy as a CONNECT to where
  the app really called, with three headers: Hotline-Capture with the
  version, and the process number and program path of the app. The tunnel
  then carries the app's own bytes, so the proxy serves it like an
  intercepted connection: it reads the site name from the TLS handshake,
  shows a certificate for it signed by the listen.capture issuer, and handles
  each call inside like any other call, with hotline.ingress capture
  (Peter, 2026-11-10).

  The extension sends its CONNECT to a socket file of its own,
  listen.capture, which only the proxy's user and root can open; the
  extension runs as root. The proxy believes the capture headers only
  there. On the proxy port they are ignored (Peter, 2026-11-12). The
  socket file has its own issuer and passthrough list, so Hotline on a Mac
  can take captured connections only, with no port open. The tests need no
  Mac: the test extension is a program that sends the same CONNECT to the
  socket file.

  Background:
    Given an interception issuer named customer in issuers, which listen.connect picks
    And listen.capture is a socket file, whose issuer is customer too
    And an app that trusts the interception issuer

  Rule: Captured calls are handled like any other call

    Scenario: A call reaches the vendor unchanged, and the record names the program
      When the extension opens a captured tunnel to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call
      Then the test vendor got the call over https with its Host, path, query, headers and body unchanged
      And the record says hotline.ingress capture and ID not-passed
      And the record says the program is process 4242 at "/usr/bin/curl"

    Scenario: The extension names the address, and the certificate names the site in the handshake
      When the extension opens a captured tunnel to the address of api.stripe.com:443 for process 4242 at "/usr/bin/curl" and the app names api.stripe.com in the handshake
      Then the app sees a certificate for api.stripe.com signed by the interception issuer

    Scenario: The port in the CONNECT decides where the proxy connects
      When the extension opens a captured tunnel to api.talon.one:8443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call with the header "Host: api.talon.one:8443"
      Then the test vendor got the call over https on port 8443

    Scenario: A handshake with no site name uses the address in the CONNECT
      When the extension opens a captured tunnel to the address of api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names no site in the handshake
      Then the app sees a certificate for the IP api.talon.one signed by the interception issuer
      When the app sends its call
      Then the proxy connected to the address of api.talon.one
      And the record says hotline.ingress capture and ID not-passed

  Rule: Hosts in passthrough are relayed untouched

    Scenario: A captured call to a host in listen.capture passthrough is relayed by its name
      Given listen.capture lists pinned.example.com in passthrough
      When the extension opens a captured tunnel to the address of pinned.example.com:443 for process 4242 at "/usr/bin/curl" and the app names pinned.example.com in the handshake
      Then the proxy dialed pinned.example.com by its name, never the address the app chose
      And the vendor's own certificate reaches the app, not one from the interception issuer
      When the app closes its connections
      Then the capture record has hotline.ingress capture, server.address pinned.example.com, hotline.intercept.mode passthrough, hotline.intercept.reason passthrough, process.pid 4242 and process.executable.path /usr/bin/curl

    Scenario: A listed host is relayed even when the handshake names no site
      The address in the CONNECT stands in for the site name, and it is
      checked against the list too, so a pinned app is never intercepted.
      Given listen.capture lists pinned.example.com in passthrough
      When the extension opens a captured tunnel to pinned.example.com:443 for process 4242 at "/usr/bin/curl" and the app names no site in the handshake
      Then the proxy dialed pinned.example.com by its name, never the address the app chose
      And the vendor's own certificate reaches the app, not one from the interception issuer
      When the app closes its connections
      Then the capture record has hotline.ingress capture, server.address pinned.example.com, hotline.intercept.mode passthrough and hotline.intercept.reason passthrough

    Scenario: A host in listen.connect passthrough only is intercepted when captured
      Given listen.connect lists pinned.example.com in passthrough
      When the extension opens a captured tunnel to pinned.example.com:443 for process 4242 at "/usr/bin/curl" and the app names pinned.example.com in the handshake
      And the app sends its call
      Then the record says hotline.ingress capture and ID not-passed

    Scenario: An ordinary tunnel to a host in listen.connect passthrough is relayed
      Given listen.connect lists *.example.com in passthrough
      And the app also trusts the vendors' own issuer
      When the app opens a tunnel to pinned.example.com:443
      Then the vendor's own certificate reaches the app, not one from the interception issuer
      When the app closes its connections
      Then the capture record has hotline.ingress connect, server.address pinned.example.com, hotline.intercept.mode passthrough and hotline.intercept.reason passthrough

    Scenario: A passthrough pattern that is not a name is refused
      Given listen.connect lists a passthrough pattern that is not a name or a *. suffix
      When the proxy loads the config
      Then the config is refused with a message naming the problem

  Rule: Connections that cannot be intercepted are recorded with the program

    Scenario: An app that does not trust the issuer
      Given an app that does not trust the interception issuer
      When the extension opens a captured tunnel to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      Then TLS inside the tunnel fails to set up
      And nothing is sent to any vendor
      And the capture record has hotline.integration.id not-passed, server.address api.talon.one, error.type tls_untrusted_by_app, process.pid 4242 and process.executable.path /usr/bin/curl

    Scenario: Plain text in a captured tunnel
      When the extension opens a captured tunnel to api.talon.one:443 for process 4242 at "/usr/bin/curl" and sends plain HTTP bytes
      Then the capture record has hotline.ingress capture, error.type not_tls and process.pid 4242

  Rule: The capture headers are not trusted

    Scenario Outline: Capture headers that are not usable are refused
      When the extension sends a CONNECT to api.talon.one:443 with <headers>
      Then the proxy answers 400 with a Proxy-Status header
      And the record says hotline.ingress capture and ID not-passed

      Examples:
        | headers                                |
        | an unknown capture version             |
        | capture version 1, from before the socket file |
        | a process number that is not a number  |
        | process number 0                       |
        | the capture header twice               |
        | two process numbers                    |
        | a program path over 4096 bytes         |

    Scenario: Capture headers on the proxy port are ignored
      Only the socket file is for captured connections, so no other program
      on this machine, and nothing forwarded to the proxy port, can name a
      program.
      When a program on this machine opens a tunnel on the proxy port to api.talon.one:443 with the capture headers for process 4242 at "/usr/bin/curl"
      And the app sends its call
      Then the record says hotline.ingress connect and ID not-passed
      And the record names no program

    Scenario: A CONNECT with a port that is not usable is refused
      When the extension sends a CONNECT to api.talon.one:0 with good capture headers
      Then the proxy answers 400 with a Proxy-Status header
      And the record says hotline.ingress capture and ID not-passed
      And nothing is sent to any vendor

    Scenario: Only CONNECT is taken on the capture socket
      When a program sends GET http://api.talon.one/ on the capture socket
      Then the proxy answers 405 with a Proxy-Status header
      And nothing is sent to any vendor

  Rule: The socket file belongs to the proxy

    Scenario: Only the proxy's own user can open the socket file
      When the proxy starts
      Then the capture socket file can be read and written by its owner only

    Scenario: A socket file left by an earlier run is replaced
      Given a socket file nobody listens on is left at the capture path
      When the extension opens a captured tunnel to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call
      Then the record says hotline.ingress capture and ID not-passed

    Scenario: A capture path another program listens on stops the proxy from starting
      Given another program listens on the capture path
      When the proxy loads the config
      Then the proxy refuses the config, saying listen.capture is in use

    Scenario: A plain file at the capture path is left alone
      Given a plain file is at the capture path
      When the proxy loads the config
      Then the proxy refuses the config, saying is not a socket file, so the proxy leaves it
      And the plain file at the capture path is unchanged

    Scenario: A capture path whose folder cannot be made stops the proxy from starting
      Given a plain file is where the capture path's folder should be
      When the proxy loads the config
      Then the proxy refuses the config, saying listen.capture

    Scenario: A renewed issuer is used without a restart
      Given the extension has opened a captured tunnel to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      When the files of the issuer named customer are replaced with a second issuer
      And the proxy checks its files
      And the extension opens a captured tunnel to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      Then the app sees a certificate for api.talon.one signed by the second issuer

    Scenario: A socket file the config no longer has takes no more tunnels
      Given the proxy starts
      When the config file drops listen.capture
      And the proxy checks its files
      And the extension sends a CONNECT to api.talon.one:443 with good capture headers
      Then the proxy answers 503 with a Proxy-Status header
      And nothing is sent to any vendor

    Scenario: The socket file is removed at stop
      When the proxy starts
      And the proxy stops
      Then there is no file at the capture path

  Rule: listen.capture is checked with the config

    Scenario Outline: A wrong listen.capture is refused
      Given listen.capture <setting>
      When the proxy loads the config
      Then the proxy refuses the config, saying <problem>

      Examples:
        | setting                                         | problem                                                                       |
        | names no issuer                                 | listen.capture must name an issuer, as issuer: name                           |
        | names an issuer that is not in issuers          | listen.capture names the issuer gone, which is not in issuers                 |
        | lists a passthrough pattern that is not a name  | listen.capture: passthrough "api.*.com" must be a host name or "*." and a suffix |
        | is a path of 120 bytes                          | listen.capture: the path is longer than 103 bytes                             |
        | is a relative path                              | listen.capture must be an absolute path or start with ~/                      |

    Scenario: A path starting with ~/ is in the home folder
      Given listen.capture is ~/hotline/capture.sock
      When the proxy starts
      Then the capture socket is at hotline/capture.sock in the home folder

  Rule: Hotline can take captured connections only

    Scenario: With listen.capture alone, the proxy opens no port
      Given the config has no listen.proxy and no listen.connect
      When the extension opens a captured tunnel to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call
      Then the record says hotline.ingress capture and ID not-passed
      And the record says the program is process 4242 at "/usr/bin/curl"
      And nothing listens on the proxy port

    Scenario: listen.connect still needs listen.proxy
      Given the config has listen.connect but no listen.proxy
      When the proxy loads the config
      Then the proxy refuses the config, saying listen.tls and listen.connect need a listen.proxy address

  Rule: The extension's check that the proxy is there leaves no trace

    Scenario: A connection that sends nothing makes no record
      When a program opens the capture socket and closes it without sending anything
      Then no record is sent
