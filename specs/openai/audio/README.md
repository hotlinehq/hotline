# OpenAI audio

`2026-11-24.openapi.yaml` holds OpenAI's two calls that upload audio: `POST /v1/audio/transcriptions` and `POST /v1/audio/translations`. Both send a `multipart/form-data` body: the audio file and a few text parts, such as `model` and `language`.

Written by hand on 2026-11-24, from OpenAI's api reference and its published spec (github.com/openai/openai-openapi). That file is written for OpenAPI 3.1, is about 4 MB and has no version of its own, so only these two calls are copied, in 3.2. OpenAI names no api version, so the file is named after the day it was written.

What differs from OpenAI's file:

- The audio is `contentMediaType: application/octet-stream`, the 3.2 way, instead of the `format: binary` OpenAI's file uses. Either makes it a file part, which the proxy counts and never reads.
- `encoding` lists the file types OpenAI takes, so a wrong one (an image, say) is reported. The list holds `video/mp4`, `video/mpeg`, `video/webm` and `application/octet-stream` as well as `audio/*`, since an SDK guesses a file's type from its name: Python's guess for `.mp4`, `.mpeg` and `.webm` is video, `.wav` is `audio/x-wav`, and an unknown name gives `application/octet-stream` (checked with Python's `mimetypes`, 2026-11-24).
- The request schemas do not refuse unknown parts (no `additionalProperties: false`), as OpenAI's own file does not, since OpenAI adds parts often.
- Only the `text` of a transcription is described in the response; the rest of `verbose_json` and `diarized_json` is left open.
