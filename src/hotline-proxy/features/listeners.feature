Feature: Listeners
  Every port or socket file the proxy opens is a listener, named in the
  listeners section, with an address (tcp://host:port or unix://path), a
  protocol (http, the default, or tcp) and the parts it has: tls, connect,
  transparent or capture. A listener with no part is a forward proxy port.
  The admin port is its own section, never a listener, so apps never reach
  it by accident (Peter, 2026-11-13).

  Certificates live in the certs section under a name. The place that uses
  one says how, with mode: sign makes a new certificate for each name an
  app calls, signed with a certificate authority (a certificate that may
  sign others); serve shows the certificate as it is. tls serves unless it
  says mode: sign; connect, transparent and capture always sign. A listener
  may name one cert for all its parts, and a part's own cert wins.

  Background:
    Given an issuer named customer in certs
    And a proxy certificate for the name hotline-proxy from the test issuer
    And an app that trusts the interception issuer

  Rule: A config may have several listeners

    Scenario: Two forward listeners each take calls
      Given the listeners are:
        """
        apps:
          address: tcp://{apps}
        tools:
          address: tcp://{tools}
          protocol: http
        """
      When the app calls Talon.One through the listener apps
      And the app calls Talon.One through the listener tools
      Then the proxy sent 2 calls to Talon.One

    Scenario: One cert on the listener serves both tls and connect
      Given the certs are:
        """
        shop: { cert: ./ca/cert.pem, key: ./ca/key.pem }
        """
      And the listeners are:
        """
        apps:
          address: tcp://{apps}
          cert: shop
          tls: { mode: sign }
          connect:
        """
      When an app that trusts the interception issuer opens TLS to the listener apps asking for the name hotline.local
      Then the app sees a certificate for hotline.local signed by the interception issuer

    Scenario: tls with mode sign signs for the address when the app names no site
      Given the certs are:
        """
        shop: { cert: ./ca/cert.pem, key: ./ca/key.pem }
        """
      And the listeners are:
        """
        apps:
          address: tcp://{apps}
          tls: { cert: shop, mode: sign }
        """
      When an app that trusts the interception issuer opens TLS to the listener apps asking for no name
      Then the app sees a certificate for the local address 127.0.0.1 signed by the interception issuer

    Scenario: tls with mode sign on a socket file signs for localhost
      Given the certs are:
        """
        shop: { cert: ./ca/cert.pem, key: ./ca/key.pem }
        """
      And the listeners are:
        """
        apps:
          address: tcp://{apps}
        local:
          address: unix://{dir}/local.sock
          tls: { cert: shop, mode: sign }
        """
      When an app that trusts the interception issuer opens TLS to the socket listener local asking for no name
      Then the app sees a certificate for the name localhost signed by the interception issuer

    Scenario: tls shows a fixed certificate by default
      Given the certs are:
        """
        team: { cert: ./tls/cert.pem, key: ./tls/key.pem }
        """
      And the listeners are:
        """
        apps:
          address: tcp://{apps}
          tls: { cert: team }
        """
      When an app opens TLS to the listener apps asking for the name api.talon.one
      Then the proxy shows its own certificate

  Rule: Transparent and capture listeners can relay bytes untouched

    Scenario: A transparent listener with protocol tcp relays what it gets
      Nothing is read, so a database or a server that speaks first works.
      Given pinned.example.com on port 6379 answers "+PONG" to what it gets
      And the original destination of the connection is 192.0.2.250:6379
      And the listeners are:
        """
        apps:
          address: tcp://{apps}
        sidecar:
          address: tcp://{sidecar}
          protocol: tcp
          transparent: { integration: ci-lint }
        """
      When the app sends "PING" to the listener sidecar
      Then the app gets "+PONG" back
      And pinned.example.com got "PING"
      And the intercept record has hotline.ingress transparent, server.address 192.0.2.250, hotline.intercept.mode passthrough and hotline.intercept.reason tcp

    Scenario: A capture listener with protocol tcp relays every port
      Given the capture listener has protocol tcp and no cert
      And pinned.example.com on port 443 answers "+PONG" to what it gets
      When the extension opens a captured tunnel to pinned.example.com:443 for process 4242 at "/usr/bin/redis-cli" and the app sends "PING"
      Then the app gets "+PONG" back
      And the capture record has hotline.ingress capture, server.address pinned.example.com, hotline.intercept.mode passthrough and hotline.intercept.reason tcp

  Rule: The admin port may take TLS

    Scenario: The health checks answer over TLS
      Given the certs are:
        """
        team: { cert: ./tls/cert.pem, key: ./tls/key.pem }
        """
      And the admin port has tls: { cert: team }
      When a health check asks /readyz over TLS
      Then it gets 200

  Rule: The listeners are checked with the config

    Scenario Outline: A listener that cannot work stops the proxy at start
      Given the certs are:
        """
        shop: { cert: ./ca/cert.pem, key: ./ca/key.pem }
        team: { cert: ./tls/cert.pem, key: ./tls/key.pem }
        """
      And the listeners are:
        """
        <listener>
        """
      When the proxy loads the config
      Then the proxy refuses the config, saying <problem>

      Examples:
        | listener                                                               | problem                                                                       |
        | apps: { address: "127.0.0.1:{apps}" }                                  | listener apps: address 127.0.0.1                                              |
        | apps: { address: "tcp://127.0.0.1" }                                   | listener apps: address tcp://127.0.0.1 must be tcp://host:port                |
        | apps:                                                                  | listener apps needs an address                                                |
        | apps: { address: "tcp://{apps}", protocol: udp }                       | listener apps: protocol must be http or tcp                                   |
        | apps: { address: "tcp://{apps}", protocol: tcp }                       | listener apps: protocol tcp is only for transparent and capture listeners     |
        | apps: { address: "tcp://{apps}", tls: { cert: shop } }                 | listener apps: tls: cert shop is a certificate authority; write mode: sign    |
        | apps: { address: "tcp://{apps}", tls: { cert: team, mode: sign } }     | listener apps: tls: cert team is not a certificate authority, so it cannot sign |
        | apps: { address: "tcp://{apps}", tls: { cert: team, mode: show } }     | listener apps: tls: mode must be serve or sign                                |
        | apps: { address: "tcp://{apps}", connect: { cert: shop, mode: serve } } | listener apps: connect always signs a certificate for each site               |
        | apps: { address: "tcp://{apps}", connect: { cert: shop, mode: copy } }  | listener apps: connect: mode must be sign                                     |
        | apps: { address: "tcp://{apps}", connect: yes }                        | listener apps: connect: write its settings as key: value                      |
        | apps: { address: "tcp://{apps}", connect: { passthrough: x } }         | listener apps: connect: yaml                                                  |
        | apps: { address: "tcp://{apps}", tls: { cert: team }, transparent: { cert: shop } } | transparent and capture each need a listener of their own             |
        | apps: { address: "unix:///tmp/x.sock", transparent: { cert: shop } }   | listener apps: transparent needs a tcp:// address                             |
        | apps: { address: "tcp://{apps}", capture: { cert: shop } }             | listener apps: capture needs a unix:// socket file                            |
        | apps: { address: "tcp://{apps}", protocol: tcp, transparent: { cert: shop } } | listener apps: with protocol tcp nothing is read                       |
        | apps: { address: "tcp://{apps}" }\nother: { address: "tcp://{apps}" }  | listener apps and listener other both use the address                         |
        | apps: { address: "tcp://{admin}" }                                     | admin and listener apps both use the address                                  |

    Scenario Outline: Settings a listener does not use start with a warning
      Given the listeners are:
        """
        <listener>
        """
      When the proxy loads the config
      Then the proxy starts and logs a warning naming <name>

      Examples:
        | listener                                                      | name                                 |
        | apps: { address: "tcp://{apps}", cert: customer }             | cert is not used                     |
        | apps: { address: "tcp://{apps}", connect: { cert: customer, passthru: [a.com] } } | field passthru is not a setting |

    Scenario: The admin address is checked like a listener's
      Given the admin port has the address "8081"
      When the proxy loads the config
      Then the proxy refuses the config, saying admin: address 8081 must start with tcp:// or unix://

    Scenario: A part written with nothing after it is on with its defaults
      Given the listeners are:
        """
        apps:
          address: tcp://{apps}
          cert: customer
          connect:
        """
      When the app opens a tunnel to api.stripe.com:443 through the listener apps
      Then the app sees a certificate for api.stripe.com signed by the interception issuer

  Rule: A reload changes a listener's settings, not its shape

    Scenario: A renewed sign certificate on tls is used without a restart
      Given the certs are:
        """
        shop: { cert: ./ca/cert.pem, key: ./ca/key.pem }
        """
      And the listeners are:
        """
        apps:
          address: tcp://{apps}
          tls: { cert: shop, mode: sign }
        """
      And an app that trusts the interception issuer has opened TLS to the listener apps asking for the name hotline.local
      When the files of the issuer named customer are replaced with a second issuer
      And the proxy checks its files
      And an app that trusts the interception issuer opens TLS to the listener apps asking for the name hotline.local
      Then the app sees a certificate for hotline.local signed by the second issuer

    Scenario: A renewed admin certificate is used without a restart
      Given the certs are:
        """
        team: { cert: ./tls/cert.pem, key: ./tls/key.pem }
        """
      And the admin port has tls: { cert: team }
      And the proxy starts
      When the cert and key files are replaced with a certificate that expires in 120 days
      And the proxy checks its files
      And a health check asks /readyz over TLS
      Then the admin port shows the renewed certificate
