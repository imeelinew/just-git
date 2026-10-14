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

The staged diff is sent to your configured API. Ignored files stay ignored. Failed generation leaves changes staged. Git errors pass through unchanged. A failed push leaves the local commit intact; retry with `git push`.

## Development

```sh
go test ./...
go build -o jg ./cmd/jg
```
