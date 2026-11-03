# just-git

Generate a commit message, commit, and optionally push. Go, Git, and an OpenAI-compatible API.

```sh
go install github.com/imeelinew/just-git/cmd/jg@latest
```

## Configure

Create `~/.config/just-git/config.json` (or `$XDG_CONFIG_HOME/just-git/config.json`):

```json
{
  "base_url": "https://api.openai.com/v1",
  "api_key": "your-key",
  "model": "your-model"
}
```

`JG_BASE_URL`, `JG_API_KEY`, and `JG_MODEL` override the file. The base URL must include the API version path when required. The key is optional for local servers.

For DeepSeek or APIs supporting its thinking parameter, add `"thinking": "enabled"` or `"thinking": "disabled"` to the config. Omit it to use the provider's default. It is sent as `{"thinking":{"type":"enabled"}}` or `{"thinking":{"type":"disabled"}}`.

Set `"reasoning_effort": "low"`, `"high"`, or `"max"` for DeepSeek; other providers accept their own effort values. Omit it to use the provider's default. When `thinking` is `disabled`, no effort parameter is sent.

To configure a second provider, add a `fallback` object. The top-level settings
remain the primary provider; each provider has its own key, model, thinking, and
reasoning effort settings. Environment overrides apply only to the primary.

```json
{
  "base_url": "https://primary.example.com/v1",
  "api_key": "primary-key",
  "model": "primary-model",
  "fallback": {
    "base_url": "https://backup.example.com/v1",
    "api_key": "backup-key",
    "model": "backup-model"
  }
}
```

Every command starts with the primary provider. Connection failures (including
DNS errors and dropped connections), request timeouts, HTTP 408, and HTTP 5xx
trigger one attempt with the fallback provider. Network errors cannot reliably
distinguish a provider outage from a local connectivity problem. Authentication,
model, parameter, quota/rate-limit errors (including HTTP 429), invalid messages,
and user cancellation do not trigger fallback. Each provider gets its own
60-second timeout, so two attempts can take up to 120 seconds. If both fail, both
errors are reported and changes remain staged. Omit `fallback` or set it to `null`
to use only the primary provider.

## Use

```sh
jg commit
jg push
```

Both commands stage and commit all changes across the repository, including new files and deletions. `push` then runs `git push` using the existing upstream. No confirmation prompts.

Messages are short English imperative subjects, without a prefix or body:

```text
Simplify clipboard state and transfer handling
```

The staged diff is sent to your configured API. Generation shows a status line and times out after 60 seconds per provider. Ignored files stay ignored. Failed generation leaves changes staged. Git errors pass through unchanged. A failed push leaves the local commit intact; retry with `git push`.

## Development

```sh
go test ./...
go build -o jg ./cmd/jg
```
