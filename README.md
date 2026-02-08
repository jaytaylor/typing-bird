# typing-bird

[![Tests](https://github.com/jaytaylor/typing-bird/actions/workflows/test.yml/badge.svg)](https://github.com/jaytaylor/typing-bird/actions/workflows/test.yml)

![Homer's typing bird at a terminal](docs/img/typing-bird.png)

Homer's typing bird for your terminal: auto-submits text after the terminal goes idle.

## Purpose

`typing-bird` watches a tmux pane for idle output, then sends the next message (plus Enter) to that pane. It cycles messages forever and can inject itself as a bottom 5-line pane in a target tmux session.

## Install

```bash
go install github.com/jaytaylor/typing-bird/cmd/typing-bird@latest
```

## Example

```bash
typing-bird -i -t 20s tpu 'proceed and keep making forward progress, use good judgement and stay focused on achieving your high level go' 'keep doing, do a great job buddy'
```
