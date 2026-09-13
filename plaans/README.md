# plaans

A small static site for San Francisco event plans, hosted on
[Fly.io](https://fly.io) as the `plaans` app.

| Path | Page |
| --- | --- |
| `/` | **Fog City Datebook** — the SF Events calendar viewer (`public/index.html`) |
| `/weekend` | **Thomas's Weekend, Sept 11–13** — the weekend brief (`public/weekend.html`) |

Pages are plain HTML in `public/`, served by nginx from a tiny container.
There is no build step: edit a file, push to `main`, and the GitHub Action
deploys it.

> **Datebook caveat.** Fog City Datebook reads Google Calendar through the
> claude.ai artifact runtime (`window.claude.use("mcp")`). Hosted here it
> renders the page shell and a "Not connected to your calendars" notice, but
> no events. To show events on Fly it needs a data source of its own, for
> example a small server that reads the calendar's public ICS feed and
> serves JSON, or a script that writes `public/events.json` on a schedule.

## Moving this folder into its own repository

This folder was authored inside the `graab` repository. To make it the
`plaans` repo:

```sh
# 1. create an empty repo on GitHub named plaans (no README, no .gitignore)
# 2. from a graab checkout on the branch that carries plaans/:
git subtree split --prefix=plaans -b plaans-main
git push git@github.com:actonric/plaans.git plaans-main:main
git branch -D plaans-main
```

Then clone `actonric/plaans`, run the first deploy above, and add the
`FLY_API_TOKEN` secret. Delete `plaans/` from `graab` afterwards.

## Local preview

```sh
# quickest
python3 -m http.server 8080 --directory public

# exactly what Fly runs
docker build -t plaans .
docker run --rm -p 8080:8080 plaans
```

Then open <http://localhost:8080/> and <http://localhost:8080/weekend>.

## First deploy

```sh
brew install flyctl          # or https://fly.io/docs/flyctl/install/
fly auth login
fly launch --copy-config --no-deploy   # creates the app named in fly.toml
fly deploy
fly open
```

The app scales to zero when idle (`min_machines_running = 0`) and starts on
the first request, so it costs nothing while nobody is looking at it.

## Deploys from GitHub

`.github/workflows/deploy.yml` runs `flyctl deploy` on every push to `main`.
It needs one repository secret:

```sh
fly tokens create deploy -x 999999h     # prints a FlyV1 token
```

Add it as `FLY_API_TOKEN` under **Settings → Secrets and variables → Actions**.

`.github/workflows/ci.yml` builds the container and smoke-tests both pages on
pull requests and non-`main` branches.

## Layout

```
Dockerfile               nginx:alpine + public/
nginx.conf               listens on 8080, /healthz, clean /weekend URL
fly.toml                 app name, region (sjc), autoscale-to-zero
public/index.html        Fog City Datebook
public/weekend.html      Thomas's Weekend, Sept 11–13
.github/workflows/       deploy.yml (main → Fly), ci.yml (build + smoke test)
```
