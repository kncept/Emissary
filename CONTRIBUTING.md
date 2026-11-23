# Contributing to Emissary

Thanks for helping improve Emissary. By contributing, you agree that your
contributions are licensed under the [MIT License](LICENSE).

## Before you start

- Check the open issues and pull requests to avoid duplicating work.
- For larger changes, open an issue first so the approach can be agreed before
  you spend time on it.
- Read [Development](docs/DEVELOPMENT.md) for build and local-run instructions,
  [Testing](docs/TESTING.md) for the test strategy, and
  [Configuration](docs/CONFIGURATION.md) when adding or changing a pluggable
  backend.

## Get a local copy

If you have write access to the main repository:

```sh
git clone https://github.com/kncept/Emissary.git
cd Emissary
git switch -c describe-your-change
```

Otherwise, fork [`kncept/Emissary`](https://github.com/kncept/Emissary) on
GitHub, then clone your fork and add the upstream remote:

```sh
git clone https://github.com/<your-account>/Emissary.git
cd Emissary
git remote add upstream https://github.com/kncept/Emissary.git
git fetch upstream
git switch -c describe-your-change upstream/main
```

## Make your changes

- Keep each change focused on one problem; split unrelated work into separate
  pull requests.
- Follow the conventions in [Development](docs/DEVELOPMENT.md). In particular,
  do not use `npx`: CLI tools must be declared dependencies and run through
  `npm run`.
- Add or update tests alongside the code. Run `./run.sh test` before pushing.
- If you change an OpenAPI spec in [`api/`](api/), run
  `./run.sh generate:api` and commit the regenerated Go and TypeScript trees.
  `./run.sh check:generated` and CI verify that those artifacts are current.
- Update documentation when behaviour, configuration or interfaces change.

## Commit history

**Do not squash commits.** Keep a logical chain of small commits that shows how
the change was arrived at, so that problems can be debugged later with tools
such as `git log` and `git bisect`. For example, separate investigation fixes,
the main change, new tests and documentation instead of combining everything
into one commit.

- Write descriptive commit messages that explain the motivation, not just the
  diff.
- Avoid mixing unrelated changes in a single commit.
- If your branch falls behind, rebase or merge without squashing your commits.
- When responding to review feedback, add follow-up commits rather than
  amending or squashing earlier commits.

## Raise a pull request

Push your branch:

```sh
git push -u origin describe-your-change
```

Then open a pull request **against `main`** at
[`kncept/Emissary`](https://github.com/kncept/Emissary). In the description:

- summarise what changed and why;
- call out any migration, configuration or API impact;
- list how you tested it, including the commands you ran;
- link any related issues.

CI runs tests, the JMAP conformance gate and the generated-code freshness
check. Resolve failures before requesting review. Maintainers will merge the
pull request with the commit history preserved.
