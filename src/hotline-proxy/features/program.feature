@e2e
Feature: The hotline-proxy program
  The proxy runs as one program, hotline-proxy, with three commands: run
  starts the proxy, validate only checks a config, and version prints the
  version. The config file is given only with --config. --trust-file adds
  trusted certificate issuers on top of the system's own. The program's own
  log lines go to standard error. Every command takes --format: console,
  the default, writes plain lines for a person, and json writes one JSON
  object per line, for log collectors (Peter, 2026-11-12). Exit codes: 0
  after a clean stop, 1 for any failure, 2 for a wrong command line. These
  scenarios start the built program and look only at its output, its exit
  code and the network (Peter, 2026-10-28).

  Scenario: The version command prints the version
    When hotline-proxy is run with "version"
    Then it exits with code 0
    And standard output starts with "hotline-proxy "

  Scenario: The version given when the program is built is printed
    The image build passes the release's git tag, such as v0.1.0, so the
    program in the image prints it (Peter, 2026-11-01). The downloadable
    program file has the same name, hotline-proxy (Peter, 2026-11-10).

    Given hotline-proxy is built with version "v0.1.0"
    When hotline-proxy is run with "version"
    Then it exits with code 0
    And standard output is "hotline-proxy v0.1.0"

  Scenario: The git commit is printed with the version
    The build passes the commit it was built from, so a downloaded program
    says exactly which code it is. Twelve characters are shown (Peter,
    2026-11-10).

    Given hotline-proxy is built with version "v0.1.0" and commit "0123456789abcdef0123456789abcdef01234567"
    When hotline-proxy is run with "version"
    Then it exits with code 0
    And standard output is "hotline-proxy v0.1.0 (commit 0123456789ab)"

  Scenario: A version that already is the commit is printed once
    The edge build uses the commit as its version.

    Given hotline-proxy is built with version "0123456789abcdef0123456789abcdef01234567" and commit "0123456789abcdef0123456789abcdef01234567"
    When hotline-proxy is run with "version"
    Then it exits with code 0
    And standard output is "hotline-proxy 0123456789abcdef0123456789abcdef01234567"

  Scenario: The version command ignores extra arguments
    A script that probes the version with an extra word still gets it, as
    before --format was added.

    When hotline-proxy is run with "version x"
    Then it exits with code 0
    And standard output starts with "hotline-proxy "

  Scenario: With --format json, the version is a JSON object
    Given hotline-proxy is built with version "v0.1.0" and commit "0123456789abcdef0123456789abcdef01234567"
    When hotline-proxy is run with "version --format json"
    Then it exits with code 0
    And standard output is the JSON {"version":"v0.1.0","commit":"0123456789ab"}

  Scenario Outline: A format other than console or json is refused with exit code 2
    When hotline-proxy is run with "<command> --format yaml"
    Then it exits with code 2
    And standard error says "for flag -format: must be console or json"

    Examples:
      | command                                  |
      | version                                  |
      | validate --config hotline-proxy.yaml     |
      | overlay --from from.yaml --to to.yaml    |

  Scenario Outline: Help is printed on request
    When hotline-proxy is run with "<command> --help"
    Then it exits with code 0
    And standard error says "<flag>"

    Examples:
      | command | flag              |
      | run     | -shutdown-timeout |
      | version | -format           |

  Scenario Outline: A wrong command line is refused with exit code 2
    When hotline-proxy is run with "<arguments>"
    Then it exits with code 2
    And standard error says "<message>"

    Examples:
      | arguments                                  | message                         |
      |                                            | usage: hotline-proxy            |
      | start                                      | unknown command "start"         |
      | run                                        | --config is required            |
      | validate                                   | --config is required            |
      | run --config hotline-proxy.yaml --colour   | flag provided but not defined   |
      | run --config hotline-proxy.yaml --log-level loud | invalid value "loud"      |
      | run --config hotline-proxy.yaml extra      | unexpected argument "extra"     |

  Scenario: validate accepts a valid config
    Given a valid config file
    When hotline-proxy is run with "validate --config hotline-proxy.yaml"
    Then it exits with code 0
    And every line on standard error is a plain line with time, level and message
    And standard error says "INFO  config is valid  config=hotline-proxy.yaml"
    And nothing listens on the config's proxy address

  Scenario: validate --format json writes JSON
    Given a valid config file
    When hotline-proxy is run with "validate --format json --config hotline-proxy.yaml"
    Then it exits with code 0
    And every line on standard error is a JSON object with time, level and msg
    And standard error says "config is valid"

  Scenario: validate refuses a config whose TLS key file is missing
    Given a config file whose tls key file does not exist
    When hotline-proxy is run with "validate --config hotline-proxy.yaml"
    Then it exits with code 1
    And standard error says "cert app: the cert or key file cannot be read"

  Scenario: validate lists every problem of a config
    Given a config file with no listeners and no exporters
    When hotline-proxy is run with "validate --config hotline-proxy.yaml"
    Then it exits with code 1
    And standard error says "ERROR config refused: config: invalid: there are no listeners, so the proxy would take no calls"
    And standard error says "ERROR config refused: config: invalid: there are no exporters"

  Scenario Outline: A trust file the program cannot use is refused
    Given a valid config file
    And <trust file>
    When hotline-proxy is run with "<command> --config hotline-proxy.yaml --trust-file trust.pem"
    Then it exits with code 1
    And standard error says "<message>"

    Examples:
      | command  | trust file                                 | message                     |
      | validate | no file trust.pem                          | trust file                  |
      | run      | a file trust.pem that holds no certificate | holds no certificate        |

  Scenario: run refuses a config with problems
    Given a config file with no listeners and no exporters
    When hotline-proxy is run with "run --config hotline-proxy.yaml"
    Then it exits with code 1
    And standard error says "there are no listeners, so the proxy would take no calls"

  Scenario: run stops with exit code 1 when the proxy port is taken
    Given a valid config file
    And something else listens on the config's proxy address
    When hotline-proxy is run with "run --config hotline-proxy.yaml"
    Then it exits with code 1
    And standard error says "address already in use"

  Scenario: run logs plain lines to standard error
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor
    When the program gets SIGTERM
    Then it exits with code 0
    And every line on standard error is a plain line with time, level and message
    And standard error says "INFO  hotline proxy started  listener.proxy="

  Scenario: run --format json logs to standard error as JSON
    The image and the Kubernetes sidecar run with --format json, for log
    collectors (Peter, 2026-11-12).

    Given a fake OTLP receiver
    And the hotline-proxy program runs with "--format json" and integration test-shop for the test vendor
    When the program gets SIGTERM
    Then it exits with code 0
    And every line on standard error is a JSON object with time, level and msg
    And standard error says "hotline proxy started"

  Scenario: The log level can be lowered
    Given a fake OTLP receiver
    And the hotline-proxy program runs with "--log-level warn" and integration test-shop for the test vendor
    When the program gets SIGTERM
    Then it exits with code 0
    And standard error does not say "hotline proxy started"

  Scenario Outline: A stop signal lets the call in progress finish
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor
    And the test vendor holds its answer
    When an app's call is in progress and the program gets <signal>
    And the test vendor then answers
    Then the app gets the test vendor's answer
    And it exits with code 0
    And the receiver got the call's record

    Examples:
      | signal  |
      | SIGTERM |
      | SIGINT  |

  Scenario: A stop that runs out of time exits with code 1
    Given a fake OTLP receiver
    And the hotline-proxy program runs with "--shutdown-timeout 1s" and integration test-shop for the test vendor
    And the test vendor holds its answer
    When an app's call is in progress and the program gets SIGTERM
    Then it exits with code 1 within 5 seconds
    And standard error says "stop ran out of time"

  Scenario: A second signal stops the program at once
    The usual stop waits up to 25 seconds; a second signal ends it now.
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor
    And the test vendor holds its answer
    When an app's call is in progress and the program gets SIGTERM
    And the program gets SIGTERM again
    Then it exits with code 1 within 5 seconds
    And standard error says "stop cut short"
