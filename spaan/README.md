# spaan

Multi-year finances and budget for a family with kids. Put in who you are,
what comes in, what goes out and when, and spaan rolls it forward year by
year so you can see how daycare, school, a house and university stack up
against income over the next couple of decades.

A static site hosted on [Fly.io](https://fly.io) as the `spaan` app, in the
same shape as `plaans`.

| Path | What |
| --- | --- |
| `/` | the planner (`public/index.html` + `public/app.js`) |
| `/engine.js` | the projection engine, a dependency-free ES module |
| `/example-plan.json` | a sample household to start from |

There is no build step and no server-side state. The plan lives in the
browser's `localStorage`; **Export JSON** / **Import JSON** move it between
devices or into version control.

## The model

A plan is a JSON document (see `public/example-plan.json`):

- **people** with a birth year and a role (`adult` / `child`).
- **incomes** and **expenses**: an annual amount in start-year money plus a
  `from` / `to` window. Expenses carry a category.
- **oneOffs**: a single year's cost (house deposit, car). Negative for money in.
- **settings**: start year, horizon, inflation, return on savings, opening
  balance, currency.

Timing is the point of the app. A window edge is either a calendar year or a
person's age, so `daycare` runs `thomas@1` to `thomas@4` and `university`
runs `thomas@18` to `thomas@21`; add a second child and their lines move with
their own birth year. Amounts rise with inflation unless a line is marked
unindexed (a fixed-rate mortgage) or has its own growth rate (a salary).
The balance earns the return rate each year and the year's net is added at
year end.

`engine.js` exports `project(plan)` which returns one row per year (ages,
income, spending, one-offs, net, balance, per-line and per-category
breakdowns) plus a summary (final balance, lowest point, first year the
balance goes negative). It is pure and has no browser dependencies, so it can
be reused from Node or another front end.

## Local preview

```sh
# tests for the engine
node --test "test/**/*.test.mjs"

# quickest
python3 -m http.server 8080 --directory public

# exactly what Fly runs
docker build -t spaan .
docker run --rm -p 8080:8080 spaan
```

Then open <http://localhost:8080/> and click **Load example**.

## First deploy

```sh
brew install flyctl          # or https://fly.io/docs/flyctl/install/
fly auth login
fly launch --copy-config --no-deploy   # creates the app named in fly.toml
fly deploy
fly open
```

The app scales to zero when idle (`min_machines_running = 0`) and starts on
the first request.

## Deploys from GitHub

`.github/workflows/deploy.yml` runs `flyctl deploy` on every push to `main`.
It needs one repository secret:

```sh
fly tokens create deploy -x 999999h     # prints a FlyV1 token
```

Add it as `FLY_API_TOKEN` under **Settings → Secrets and variables → Actions**.

`.github/workflows/ci.yml` runs the engine tests, builds the container and
smoke-tests the pages on pull requests and non-`main` branches.

## Moving this folder into its own repository

This folder was authored inside the `graab` repository. To make it the
`spaan` repo:

```sh
# 1. create an empty repo on GitHub named spaan (no README, no .gitignore)
# 2. from a graab checkout on the branch that carries spaan/:
git subtree split --prefix=spaan -b spaan-main
git push git@github.com:actonric/spaan.git spaan-main:main
git branch -D spaan-main
```

Then clone `actonric/spaan`, run the first deploy above, and add the
`FLY_API_TOKEN` secret. Delete `spaan/` from `graab` afterwards.

## Layout

```
Dockerfile               nginx:alpine + public/
nginx.conf               listens on 8080, /healthz, clean URLs
fly.toml                 app name, region (sjc), autoscale-to-zero
public/index.html        page shell and styles
public/app.js            editors, projection table, balance chart, storage
public/engine.js         projection engine (pure, tested)
public/example-plan.json sample household
test/engine.test.mjs     node --test suite for the engine
.github/workflows/       deploy.yml (main → Fly), ci.yml (tests + build + smoke)
```

## Not yet

- Scenarios side by side (rent vs buy, one salary vs two).
- Taxes: incomes are after tax today.
- Monthly view and actuals vs plan.
- Sync between devices beyond export/import.
