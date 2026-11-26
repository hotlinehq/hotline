# Requirements and decisions: docs/decisions/spec-checks.md (M4, 2026-11-24).
Feature: Multipart request bodies
  A multipart/form-data body is one request body cut into named parts, such
  as an audio file and the text parts model and language. The proxy reads
  the parts as they pass, so an upload of any size is checked. Each
  property's text parts are checked against its schema, as a form is, and
  a part the spec says is JSON is checked as JSON. A file
  part, a property the spec gives format binary or a contentMediaType, is
  counted and its content type checked, but the file is never read or kept
  (Peter, 2026-11-24, Q1 to Q5).

  Rule: The text parts are checked against the spec, as a form is

    Scenario: A good audio upload passes
      Given the OpenAI audio spec from specs
      When the app uploads 300 KiB of audio/mpeg named "memo.mp3" to OpenAI's /v1/audio/transcriptions with the text parts "model=gpt-4o-transcribe&language=en"
      Then the vendor got the upload byte for byte
      And the record has no request results
      And the record says hotline.schema.status=valid
      And the record holds gen_ai.request.model = gpt-4o-transcribe

    Scenario Outline: A text part that breaks the spec is reported
      Given the OpenAI audio spec from specs
      When the app uploads 300 KiB of audio/mpeg named "memo.mp3" to OpenAI's /v1/audio/transcriptions with the text parts "<parts>"
      Then the request results are <results>

      Examples:
        | parts                                          | results                                               |
        | language=en                                    | body $.model required                                 |
        | model=gpt-4o-transcribe&temperature=warm       | body $.temperature type number                        |
        | model=gpt-4o-transcribe&response_format=mp3    | body $.response_format enum json, text, srt, verbose_json, vtt, diarized_json |

    Scenario: A number written as text passes, as in a form
      Given the OpenAI audio spec from specs
      When the app uploads 300 KiB of audio/mpeg named "memo.mp3" to OpenAI's /v1/audio/transcriptions with the text parts "model=gpt-4o-transcribe&temperature=0.2"
      Then the record has no request results

    Scenario: Parts named with brackets are read as a list
      Given the OpenAI audio spec from specs
      When the app uploads 300 KiB of audio/mpeg named "memo.mp3" to OpenAI's /v1/audio/transcriptions with the text parts "model=whisper-1&response_format=verbose_json&timestamp_granularities[]=word&timestamp_granularities[]=segment"
      Then the record has no request results

    Scenario: A missing file part is reported
      Given the OpenAI audio spec from specs
      When the app posts to OpenAI's /v1/audio/transcriptions only the text parts "model=gpt-4o-transcribe"
      Then the request results are body $.file required

    Scenario: An empty part whose boundary comes right after its headers is read as empty
      Go's multipart reader, which many vendors use, reads the part as empty,
      so the check must too. Each ~ in the body is a carriage return and a
      line feed.
      Given the OpenAI audio spec from specs
      When the app posts to OpenAI's /v1/audio/transcriptions the body "--b~Content-Disposition: form-data; name=prompt~~--b~Content-Disposition: form-data; name=model~~whisper-1~--b--" as "multipart/form-data; boundary=b"
      Then the request results are body $.file required

  Rule: A part the spec says is JSON is checked as JSON
    A property whose encoding lists a JSON type, or an object or a list of
    objects with no encoding, is sent as JSON, as OpenAPI 3.2 says (Peter,
    2026-11-24, Q6).

    Scenario: A voice call whose session is good JSON passes
      Given the OpenAI audio spec from specs
      When the app starts a voice call at OpenAI's /v1/realtime/calls with the session {"type":"realtime","model":"gpt-realtime"}
      Then the vendor got the upload byte for byte
      And the record has no request results
      And the record holds gen_ai.request.model = gpt-realtime

    Scenario: A JSON part sent twice is read as a list of both
      Given the OpenAI audio spec from specs
      When the app starts a voice call at OpenAI's /v1/realtime/calls with the session {"type":"realtime"} twice
      Then the request results are body $.session type object

    Scenario Outline: A JSON part that breaks the spec is reported
      Given the OpenAI audio spec from specs
      When the app starts a voice call at OpenAI's /v1/realtime/calls with the session <session>
      Then the request results are <results>

      Examples:
        | session             | results                                         |
        | {"type":"voice"}    | body $.session.type enum realtime, transcription |
        | {"model":"x"}       | body $.session.type required                    |
        | {"type":"realtime"  | body $.session json                             |

    Scenario: A JSON property sent with brackets is read as text
      Given the OpenAI audio spec from specs
      When the app posts to OpenAI's /v1/realtime/calls only the text parts "sdp=v%3D0&session[type]=realtime"
      Then the record has no request results

  Rule: A file part is counted, never read

    Scenario: A large upload is still checked, and passes on untouched
      Given the OpenAI audio spec from specs
      When the app uploads 20 MiB of audio/mpeg named "memo.mp3" to OpenAI's /v1/audio/transcriptions with the text parts "language=en"
      Then the vendor got the upload byte for byte
      And the request results are body $.model required

    Scenario: A file type the spec doesn't list is reported
      Given the OpenAI audio spec from specs
      When the app uploads 300 KiB of image/png named "memo.png" to OpenAI's /v1/audio/transcriptions with the text parts "model=gpt-4o-transcribe"
      Then the request results are body $.file content_type audio/*, video/mp4, video/mpeg, video/webm, application/octet-stream

    Scenario: Any file type passes when the spec lists none
      Stripe's spec says only that the file is binary.
      Given the Stripe spec from specs, with Stripe answering a new file
      When the app uploads 300 KiB of image/png named "receipt.png" to Stripe's /v1/files with the text parts "purpose=dispute_evidence"
      Then the record has no request results

    Scenario: The record lists the file part, with its file name hidden
      Given the OpenAI audio spec from specs
      When the app uploads 300 KiB of audio/mpeg named "meeting-with-jane.mp3" to OpenAI's /v1/audio/transcriptions with the text parts "model=gpt-4o-transcribe"
      Then the record lists the file part file: 307200 bytes of audio/mpeg named REDACTED
      And no record holds "meeting-with-jane.mp3"

    Scenario: A spec shows the file name with x-hotline-redact false
      Given the OpenAI audio spec from specs, with the file property marked x-hotline-redact false
      When the app uploads 300 KiB of audio/mpeg named "meeting-with-jane.mp3" to OpenAI's /v1/audio/transcriptions with the text parts "model=gpt-4o-transcribe"
      Then the record lists the file part file: 307200 bytes of audio/mpeg named meeting-with-jane.mp3

    Scenario: Under content full the body is kept as sent, file name included
      Given the OpenAI audio spec from specs, with content full and a file exporter
      When the app uploads 4 KiB of audio/mpeg named "meeting-with-jane.mp3" to OpenAI's /v1/audio/transcriptions with the text parts "model=gpt-4o-transcribe"
      Then the line's request body holds "meeting-with-jane.mp3"
      And the record lists the file part file: 4096 bytes of audio/mpeg named REDACTED

    Scenario Outline: A file part's own type is read as RFC 7578 says
      Given the OpenAI audio spec from specs
      When the app uploads 4 KiB of audio sent as "<type>" to OpenAI's /v1/audio/transcriptions with the text parts "model=gpt-4o-transcribe"
      Then the request results are <results>

      Examples:
        | type                | results                                                                                     |
        |                     | body $.file content_type audio/*, video/mp4, video/mpeg, video/webm, application/octet-stream |
        | audio/              | body $.file content_type audio/*, video/mp4, video/mpeg, video/webm, application/octet-stream |

    Scenario: A file type is matched whatever its case and parameters
      Given the OpenAI audio spec from specs
      When the app uploads 4 KiB of audio sent as "AUDIO/MP4; codecs=mp4a" to OpenAI's /v1/audio/transcriptions with the text parts "model=gpt-4o-transcribe"
      Then the record has no request results

  Rule: Stripe's file upload is checked from Stripe's whole spec

    Scenario: Stripe's bracket names are nested, as in a form
      Given the Stripe spec from specs, with Stripe answering a new file
      When the app uploads 300 KiB of application/pdf named "receipt.pdf" to Stripe's /v1/files with the text parts "purpose=dispute_evidence&file_link_data[create]=true"
      Then the record has no request results

    Scenario: A part Stripe's spec doesn't name is reported
      Given the Stripe spec from specs, with Stripe answering a new file
      When the app uploads 300 KiB of application/pdf named "receipt.pdf" to Stripe's /v1/files with the text parts "purpose=dispute_evidence&colour=red"
      Then the request results are body $.colour additionalProperties

  Rule: What can't be read is said so

    Scenario: A multipart body where the spec lists only a form is reported
      Given the Stripe spec from specs, with Stripe answering a new file
      When the app uploads 4 KiB of image/png named "logo.png" to Stripe's /v1/customers with the text parts "name=Jane"
      Then the request results are body $ content_type application/x-www-form-urlencoded

    Scenario Outline: A body that breaks the multipart framing is reported
      Each ~ in the body is a line break, a carriage return and a line feed.
      Given the OpenAI audio spec from specs
      When the app posts to OpenAI's /v1/audio/transcriptions the body "<body>" as "<type>"
      Then the request results are body $ multipart

      Examples:
        | type                            | body                                                          |
        | multipart/form-data             | --b~Content-Disposition: form-data; name=model~~whisper-1~--b-- |
        | multipart/form-data; boundary=b | --b x~Content-Disposition: form-data; name=model~~whisper-1~--b-- |
        | multipart/form-data; boundary=b | --bxyz~Content-Disposition: form-data; name=model~~whisper-1~--b-- |
        | multipart/form-data; boundary=b | --b~Content-Disposition: attachment; name=model~~whisper-1~--b-- |
        | multipart/form-data; boundary=b | --b~Content-Disposition: form-data~~whisper-1~--b--             |

    Scenario: An empty multipart body is a missing body
      Given the OpenAI audio spec from specs
      When the app posts to OpenAI's /v1/audio/transcriptions the body "" as "multipart/form-data; boundary=b"
      Then the request results are body $ required

    Scenario: A part whose headers are over 16 KiB is reported
      Given the OpenAI audio spec from specs
      When the app posts to OpenAI's /v1/audio/transcriptions a part whose headers are over 16 KiB
      Then the request results are body $ multipart

    Scenario: What comes after the closing boundary is not read
      RFC 2046 calls it the epilogue, and says to ignore it.
      Given the OpenAI audio spec from specs
      When the app uploads 300 KiB of audio to OpenAI's /v1/audio/transcriptions with the text parts "model=gpt-4o-transcribe" and 100 KiB after the closing boundary
      Then the vendor got the upload byte for byte
      And the record has no request results

    Scenario: A body whose parts never begin is reported
      Given the OpenAI audio spec from specs
      When the app posts to OpenAI's /v1/audio/transcriptions a multipart body whose boundary never comes
      Then the request results are body $ multipart

    Scenario: A body cut off in the middle of a part is reported
      Given the OpenAI audio spec from specs
      When the app posts to OpenAI's /v1/audio/transcriptions a multipart body that ends in the middle of the file
      Then the request results are body $ multipart
      And the record lists the file part file: 4096 bytes of audio/mpeg named REDACTED

    Scenario: A text part over 2 MiB is skipped, and the rest still checked
      Given the OpenAI audio spec from specs
      When the app uploads 300 KiB of audio/mpeg named "memo.mp3" to OpenAI's /v1/audio/transcriptions with a prompt of 3 MiB and no model
      Then the request results are body $.model required
      And the record says hotline.schema.skipped=body_too_large

    Scenario: A multipart body other than form-data is not what the spec lists
      Given the OpenAI audio spec from specs
      When the app posts to OpenAI's /v1/audio/transcriptions a multipart/mixed body
      Then the request results are body $ content_type multipart/form-data
