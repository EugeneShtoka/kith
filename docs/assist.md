# Assist: language models

kith has two optional language-model features. Both are off until you set them up, and they are configured separately because they involve different trade-offs.

| | Local completion model | Remote assist endpoint |
| --- | --- | --- |
| Config section | `[complete.model]` | `[assist]` |
| What it does | Suggests the next word as you type | `/summary`, `:todo`, thread naming |
| Where it runs | On your machine (llama.cpp) | Any OpenAI-compatible endpoint you name |
| What leaves the machine | Nothing | Part of a conversation, from rooms you allow |
| Turned on by | Installing the weights | Setting `endpoint` |
| Cost | About 460 MB of RAM while warm | Whatever your endpoint charges |

Word completion from your own message history and spell-checking do not need either feature. See [Writing messages](composer.md).

## Local completion model

A small model runs on your CPU and suggests the next word after you finish one. **Nothing you type is sent anywhere.** Completion has no remote option. A remote path was measured and removed because it was less accurate, slower and less private than a 360M model run locally.

### Setting it up

You need two things:

1. **llama.cpp's server**, `llama-server`, on your `PATH`. On Arch it is `pacman -S llama.cpp`, and on Homebrew `brew install llama.cpp`. A build of your own works too; point `command` at it.
2. **The weights:**

   ```sh
   kith --add-model list           # what can be installed, and what already is
   kith --add-model smollm2-360m   # 369 MB, checked against a pinned SHA-256
   ```

That is all the setup there is. Weights land in `~/.local/share/kith/models`, and the daemon finds them the next time you type. The first time completion has nothing to answer with, kith offers to install the model for you. If you untick that offer and accept, it writes `declined = true` and does not ask again.

### What it costs

The daemon starts `llama-server` on a loopback port the first time you type, and stops it after `idle` with no completions. While it is running it holds about 460 MB, and restarting it takes under half a second. A machine with the model configured but never used pays nothing.

### Using it

At a word boundary, a strip offers up to five next words. One slot always comes from this room's own vocabulary, because jargon is the one thing a general model cannot know. The best candidate is also drawn as ghost text. The keys are the same ones word completion uses:

| Key | What it does |
| --- | --- |
| `right` | Take the next word of the suggestion |
| `tab` | Take the whole suggestion |
| `alt+1` … `alt+5` | Take that option from the strip |

While the local model is active, the composer gutter shows `⌂` and the model's name. It shows `⌂ not ready` while the model cannot answer yet and `⌂ failing` when requests are failing.

### Configuration

```toml
[complete.model]
enabled = true            # on whenever the weights are installed; false keeps them unused
command = "llama-server"  # a name on PATH, or a path
model   = ""              # a manifest tag or a path to your own .gguf; empty = whichever is installed
threads = 0               # 0 = llama.cpp's default
context = 0               # token window, 0 = 1024
port    = 0               # 0 = a free loopback port
idle    = "10m"           # stop the server after this long unused; "0s" = never
startup = "20s"           # how long a spawn may take
timeout = "1s"            # how long one prediction may take
options = 4               # how many model words the strip offers, 1–5
declined = false

# Which conversation to show the model. None of it leaves the machine.
budget       = 1500       # roughly how many tokens of context
recent       = 4          # how many of the latest messages
gap          = "90m"      # a silence this long ends the conversation
before_reply = 2          # messages before the one you are replying to
```

`gap` is measured between messages, not against the clock. A conversation running from 23:50 to 00:10 counts as one conversation. When you are replying, the message you are answering is always included, however old it is.

## Remote assist endpoint

`[assist]` connects kith to a chat model for tasks a small local model cannot do: summarizing a conversation and reading across rooms. **It is off by default, and there is no default endpoint.**

**What it costs:** a request sends part of a conversation, including your words and other people's, to the endpoint. The room lists below decide which rooms can be sent, and encrypted rooms have a separate switch.

### Choosing an endpoint

Any `POST …/v1/chat/completions` endpoint works: Groq, Gemini's OpenAI-compatible endpoint, Mistral, OpenRouter, ollama or llama.cpp. A model running on your own machine sends nothing off it:

```toml
[assist]
endpoint = "http://localhost:11434/v1/chat/completions"   # ollama
model    = "llama3.1:8b"
```

A hosted endpoint needs an API key. **The key is kept in the OS keyring, never in the config file:**

```sh
kith --set-model-key groq   # prompts without echo; also reads from a pipe
```

```toml
[assist]
endpoint = "https://api.groq.com/openai/v1/chat/completions"
model    = "llama-3.1-8b-instant"
key_ref  = "groq"             # the keyring entry's name, not the key itself
```

Running `--set-model-key` again with an empty answer removes the key. The key belongs to you, not to a Matrix account, so every profile on the machine shares it. With `key_ref` empty, requests go out with no `Authorization` header, which is what a local server expects.

### Which rooms the model may see

The model only ever sees the rooms these lists allow:

| `rooms` | `except` | What is allowed |
| --- | --- | --- |
| non-empty | ignored | Only the rooms `rooms` names |
| empty | set | Every room except those |
| empty | empty | Every room |

Entries can name a single room or a whole kind of room:

| Entry | Matches |
| --- | --- |
| `!abc:example.org` | One room, by ID |
| `room:Standup` | One room, by its name in the rail (or its ID) |
| `space:Work` | Every room in that space |
| `protocol:WhatsApp` | Every room bridged from that network (`Matrix` for native rooms) |
| `dm` | Every direct message |
| `group` | Every room that is not a direct message |

**Encrypted rooms are always refused unless you also set `encrypted = true`.** This is a separate switch from the lists, because the point of an encrypted room is that its contents stay in it. Allowing "everywhere" should not quietly include encrypted rooms. If kith cannot tell whether a room is encrypted, it treats the room as encrypted.

A bare word that is neither a room ID nor one of the prefixes above is refused at startup. So is a room named in both lists.

```toml
[assist]
except    = ["dm"]          # everywhere except private conversations
encrypted = false
```

```toml
[assist]
rooms = ["space:Work"]      # one space and nothing else
```

These lists apply only to `[assist]`. [kith-mcp](mcp.md) has its own separate lists under `[agent.read]` and `[agent.write]`, using the same vocabulary.

### Limits

```toml
[assist]
budget         = 1500   # tokens of conversation a request may quote, newest first
max_per_minute = 20     # rolling-window cap on requests
timeout        = "4s"   # one request
todo_rooms     = 8      # how many rooms :todo may read
```

`budget` is the setting that decides how much of a room leaves your machine. Each task can override it; the task's own block wins.

### See what would be sent: `alt+m`

`alt+m` in the composer shows the endpoint, the model and the exact text a request from this room would contain: the prompt and the conversation the budget admits. **It sends nothing.** If the room would be refused, it says why. It is worth pressing once before you turn the endpoint on, and again after you change a prompt.

## Assist tasks

### `/summary`: what a room has been saying

Type `/summary` in a room's composer. The answer opens in a reader, not in the composer, so it is never one keystroke away from being sent to the people it summarizes.

```text
/summary              everything since your read marker; the recent conversation if you are caught up
/summary 2h           the last two hours
/summary 7d           the last week (also 2w, 3m, 1y)
/summary yesterday    or today, or a date: 2026-09-18
/summary 50           the last fifty messages
```

The time spans use the same words as the search box's `since:`. If kith cannot read a span, it refuses rather than guessing. The reader says how many messages were summarized. Your own messages are labelled `You:` in the context, so the summary can tell what you have already answered.

### `:todo`: what people are waiting on you for

`:todo` on the command line reads the rooms that have unread messages, up to `todo_rooms` of them, and lists what people are asking of you. It checks each room against the room lists before reading it. It is a `:` command because it works across rooms. `/summary` answers the same kind of question for one room.

### Thread naming

A thread with no name is labelled with a snippet of its first message. With `name_threads` on, the model is asked for a better name once the thread has enough replies:

```toml
[assist]
name_threads       = false   # off by default: the one task that runs without being asked
name_threads_after = 2       # replies a thread needs first
```

The name is written to `[[display.name]]` under `thread:<root>`, the same place a name you set yourself with `R` goes. A name you set yourself wins. If you clear a generated name, it can come back on a later run.

### Rewrite

`[assist.rewrite]` configures a task that rewrites a draft to follow an instruction. The task can be configured, but no key calls it yet.

### Prompts

Each task has its own block: `[assist.summary]`, `[assist.todo]`, `[assist.thread_name]` and `[assist.rewrite]`.

```toml
[assist.summary]
system           = ""      # empty = the built-in prompt
prompt           = ""
model            = ""      # e.g. a bigger model at the same endpoint; empty = [assist] model
budget           = 6000
max_tokens       = 1600    # includes whatever a reasoning model spends thinking
temperature      = 0.0     # 0 = the endpoint's default
reasoning_effort = "low"   # low, medium, high, or empty
stop             = []
```

Prompt templates can use these fields:

| Field | Filled with |
| --- | --- |
| `{draft}` | What you have typed |
| `{context}` | The conversation being quoted (for thread naming, the thread) |
| `{language}` | The draft's language, as the spell-checker detects it |
| `{instruction}` | What you asked for, for tasks that take one |
| `{me}` | Your display name |
| `{names}` | Every name the conversation might use for you |

## Related

- [Writing messages](composer.md): word completion and ghost text without a model
- [kith-mcp](mcp.md): giving an external AI assistant access to your account
- [Configuration](configuration.md)
