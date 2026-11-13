Feature: TLS from apps to the proxy
  With the tls part set, the app opens TLS to the proxy and sends the same
  request inside it, with the full vendor address in the request line. The
  proxy shows its own certificate, read from the cert and key files; it
  never makes one of its own. The proxy port then accepts only TLS, versions
  1.2 and 1.3, over HTTP/1.1 or HTTP/2 (see host-header.feature). Client certificates (mutual TLS) come later
  (Peter, 2026-10-28).

  Background:
    Given a proxy certificate for the name hotline-proxy from the test issuer
    And a config file whose tls part points to its cert and key files

  Scenario: A call over TLS reaches the vendor
    Given an app that trusts the test issuer
    When the app sends its call over TLS to the proxy
    Then the vendor gets the call over https
    And the app gets the vendor's status, headers and body unchanged
    And the record says hotline.ingress forward_proxy and ID talon-shop-a

  Scenario: The handshake with the app is timed on the first call of each connection
    The proxy's own cost includes setting up TLS with the app (Peter,
    2026-10-29). Later calls on the same connection skip it, like any phase
    that did not happen.
    Given an app that trusts the test issuer
    When the app sends two calls on one TLS connection
    Then the first call's trace has an "app tls handshake" span at the start of "hotline call"
    And the second call's trace has no "app tls handshake" span

  Scenario: The proxy shows its one certificate whatever name the app asks for
    When an app opens TLS to the proxy asking for the name api.talon.one
    Then the proxy shows its own certificate

  Scenario: The proxy port accepts only TLS
    When an app sends its call to the proxy port without TLS
    Then the app gets 400
    And nothing is sent to any vendor
    And no record is sent

  Scenario Outline: A connection that fails to set up TLS is not a call
    Given <app>
    When the app sends its call over TLS to the proxy
    Then the TLS setup fails
    And nothing is sent to any vendor
    And no record is sent
    And the proxy logs the failure at debug level

    Examples:
      | app                                         |
      | an app that does not trust the test issuer  |
      | an app that allows only TLS 1.1             |

  Scenario Outline: TLS versions the proxy accepts
    Given an app that trusts the test issuer and allows only <version>
    When the app sends its call over TLS to the proxy
    Then the app gets the vendor's status, headers and body unchanged
    And the connection used <version>

    Examples:
      | version |
      | TLS 1.2 |
      | TLS 1.3 |

  Scenario Outline: A bad certificate stops the proxy at start
    Given <problem>
    When the proxy loads the config
    Then the config is refused with a message naming the problem

    Examples:
      | problem                                        |
      | the cert file does not exist                   |
      | the key file holds no key                      |
      | the key does not belong to the certificate     |
      | the certificate has already expired            |
      | the tls part has a cert file but no key file     |

  Scenario: A renewed certificate is used without a restart
    Given an app that trusts the test issuer
    And the app has a connection open to the proxy
    When the cert and key files are replaced with a second certificate
    And the proxy checks its files
    Then a new connection gets the second certificate
    And the open connection still takes calls

  Scenario: A reload with the same certificate keeps it
    Given an app that trusts the test issuer
    And the proxy is running
    When the proxy is told to reload
    Then a new connection still gets the first certificate
    And the proxy does not log that a new TLS certificate is in use

  Scenario: A broken renewal keeps the old certificate
    Given an app that trusts the test issuer
    And the proxy is running
    When the key file is replaced with a key that does not belong to the certificate
    And the proxy checks its files
    Then a new connection still gets the first certificate
    And the proxy logs a warning that the reload failed, naming cert app
