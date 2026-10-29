# Contributing

Thanks for your interest! This project uses only free and open-source tools, and everything should run in Docker.

## Ground rules

- Keep every new dependency free and open source.
- New components need a `Dockerfile` so they run without local toolchains.
- Never commit secrets. Copy `.env.example` to `.env` and fill in your own values.
- Use [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, ...).

## Workflow

1. Fork and create a branch: `git checkout -b feat/my-change`
2. Make your change, with tests where practical.
3. Update the roadmap in `README.md` if you complete a task.
4. Open a pull request describing what changed and how you verified it.
