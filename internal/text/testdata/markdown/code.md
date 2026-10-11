# Parsing JSON in Go

Use `encoding/json` from the standard library.

```go
type User struct {
	Name string `json:"name"`
}
```

Decode with `json.Unmarshal`:

```go
var u User
err := json.Unmarshal(data, &u)
```

    // An indented code block.
    fmt.Println(u.Name)

<div class="note">
Prefer a decoder for streams.
</div>

| Function | Use |
| --- | --- |
| `Marshal` | encode |
| `Unmarshal` | decode |

| Option | Default |
| --- | --- |
| strict | off |

Errors wrap with <code>%w</code>, so `errors.Is` works.

```
plain fence
```
