# Self-hosting EventMesh on your own Linux PC

This guide puts EventMesh on the internet from a Linux PC at home, for free, with HTTPS. At the end you will have
a link like `https://mypc.tail1234.ts.net` that works from any phone or laptop.

Running example: you are showing the project in an interview. The interviewer opens your link on their laptop, signs
in with the demo account you made, and watches a BrewBox "order paid" event get delivered.

## How it fits together

```
Visitor's browser
   │  https://mypc.tail1234.ts.net
   ▼
Tailscale's servers (the "Funnel")            ← public entrance, handles HTTPS
   │  encrypted connection that YOUR PC opened (outgoing)
   ▼
Your Linux PC: tailscaled  ──►  127.0.0.1:8090  ──►  api container
                                                     worker container ──► customers' webhook URLs
                                                     postgres, redis   (not reachable from outside at all)
```

- **Tunnel.** Your router blocks connections that start on the internet. Instead of opening a hole in it (port
  forwarding), your PC opens a connection *out* to Tailscale, and visitors come in through that connection. Your home
  IP address stays hidden and no router setting changes.
- **Only the API is exposed**, and only on `127.0.0.1` (the PC itself). PostgreSQL and Redis have no published ports.

## What the production settings do (and why)

`docker-compose.prod.yml` is laid on top of the normal `docker-compose.app.yml`. Each change closes a specific hole:

| Setting | Problem it prevents |
| --- | --- |
| `EM_ENV=production` | The app refuses to start if any development default is still set, and the login cookie is marked `Secure` (sent over HTTPS only). |
| Random database and Redis passwords, random encryption key in `.env.prod` | The development values are in the public repo. With them, anyone who reached your database could read the webhook secrets. |
| New data volume `pgdata_prod` | The dev database (dev password) is never reused by accident. |
| Demo receiver off, `EM_SSRF_ALLOWED_HOSTS` empty | Users cannot point webhooks at machines on your home network (SSRF). |
| `EM_ALLOW_REGISTRATION=false` (after you sign up) | Strangers cannot create accounts and use your PC to send HTTP requests. |
| `EM_TRUSTED_PROXIES` + fixed Docker network | Behind a tunnel, every request seems to come from one address. Trusting the tunnel lets the API read the real visitor IP, so one person spamming the login page is rate-limited alone, not everyone. |
| Read-only containers, no Linux capabilities, `no-new-privileges` | If there were a bug in the app, the attacker could not change files or gain root inside the container. |
| Log size caps | A busy day cannot fill the disk. |
| Security headers (HSTS, `X-Frame-Options`, `nosniff`) | Browsers keep to HTTPS, and other sites cannot load your dashboard in a hidden frame (clickjacking). |

## Step 1: prepare the PC

On Ubuntu or Debian (other distributions: same idea, different package commands).

Install updates automatically, and turn on the firewall. The firewall can block everything incoming, because the
tunnel is an outgoing connection:

```bash
sudo apt update && sudo apt install -y unattended-upgrades ufw git curl
```
```bash
sudo ufw default deny incoming && sudo ufw default allow outgoing
```
```bash
sudo ufw allow ssh
```
(Skip the `ssh` line if you never log in to the PC remotely.)
```bash
sudo ufw enable
```

Stop the PC from sleeping (the site is down whenever it sleeps):

```bash
sudo systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target
```

Install Docker using the official instructions at https://docs.docker.com/engine/install/ (pick your distribution).
Then let your user run it and make it start at boot:

```bash
sudo usermod -aG docker $USER && sudo systemctl enable docker
```

Log out and back in so the group change applies.

Note: Docker publishes ports by writing its own firewall rules, which `ufw` does not see. That is why the compose
files publish the API only on `127.0.0.1`: it is never reachable from your network, firewall or not.

## Step 2: get your public address (Tailscale)

```bash
curl -fsSL https://tailscale.com/install.sh | sh
```
```bash
sudo tailscale up
```

It prints a sign-in link: log in with Google, GitHub or Microsoft (free plan). Then find your PC's name:

```bash
tailscale status --json | grep -m1 '"DNSName"'
```

It looks like `mypc.tail1234.ts.net.` Your public address is `https://mypc.tail1234.ts.net` (drop the final dot).

## Step 3: create the secrets and start EventMesh

```bash
git clone <your-repo-url> eventmesh && cd eventmesh
```
```bash
./scripts/prod.sh init https://mypc.tail1234.ts.net
```

This writes `.env.prod` with random passwords and an encryption key, readable only by your user. Git ignores it.
**Copy `.env.prod` somewhere safe** (a password manager): without `EM_PROD_ENCRYPTION_KEY`, a database backup is
missing the key that unlocks the webhook secrets inside it.

```bash
./scripts/prod.sh up
```

The first build takes a few minutes. Check it:

```bash
./scripts/prod.sh status
```

The last line should be `{"postgres":"ok","redis":"ok"}`.

## Step 4: make it public

```bash
sudo tailscale funnel --bg 8090
```

The first time, it prints a link to turn Funnel on for your account; open it, approve, and run the command again.
`--bg` keeps it running, including after reboots. Open your `https://...ts.net` address on your phone (on mobile data,
to be sure it works from outside).

## Step 5: create your account, then close registration

1. Open the site and register your account (and a demo account for interviews, if you like).
2. Close sign-up: edit `.env.prod`, set `EM_ALLOW_REGISTRATION=false`, then apply it:

```bash
./scripts/prod.sh up
```

Anyone trying to register now gets "Registration is closed on this server." Existing accounts still sign in.

## Day to day

| Task | Command |
| --- | --- |
| Is it healthy? | `./scripts/prod.sh status` |
| Follow the logs | `./scripts/prod.sh logs api worker` |
| Update to the latest code | `git pull && ./scripts/prod.sh up` (migrations run automatically) |
| Stop (data is kept) | `./scripts/prod.sh down` |
| Back up the database | `./scripts/prod.sh backup` (writes `backups/`, deletes files older than 14 days) |
| Restore a backup | `./scripts/prod.sh restore backups/<file>.sql.gz` |
| Take the site offline, keep it running locally | `sudo tailscale funnel --bg 8090 off` |

Automatic daily backup at 03:00: run `crontab -e` and add this line (use your real folder path):

```
0 3 * * * cd /home/<you>/eventmesh && ./scripts/prod.sh backup >> backups/cron.log 2>&1
```

Backups on the same disk do not survive a dead disk: copy `backups/` to another drive or cloud storage now and then.

## Optional AI features

They stay off. To turn them on, add `EM_AI_PROVIDER`, `EM_AI_API_KEY` and the `EM_FEATURE_AI_*` flags to `.env.prod`
and run `./scripts/prod.sh up`. Every visitor's question then uses your API quota, so keep registration closed.

## Limits worth knowing (and saying in an interview)

- **One machine.** If the PC, its disk or your internet connection fails, the site is down. A real production setup
  runs several copies on separate machines with a managed database.
- **Your home connection sends the webhooks.** The worker's requests come from your home IP. The SSRF checks stop
  requests to private addresses, but a determined abuser with an account could still send traffic to public sites,
  which is why registration should stay closed.
- **Trusted-proxy subnet.** The API trusts `X-Forwarded-For` from the Docker network `172.31.240.0/24`. Tailscale
  overwrites that header with the real visitor IP, so visitors cannot fake it. If that subnet clashes with your home
  network, change `EM_PROD_SUBNET` in `.env.prod` (then `./scripts/prod.sh down` and `up`).
- **No email verification or password reset.** Fine for a single owner; a public multi-user service would need them.

## If something goes wrong

- `invalid configuration: ...` in `./scripts/prod.sh logs api`: the message lists every bad setting.
- Every form shows "Origin is not allowed": `EM_PUBLIC_URL` in `.env.prod` does not exactly match the address in the
  browser (`https`, no trailing slash). Fix it and run `./scripts/prod.sh up`.
- Login works locally but not through the link: check `tailscale funnel status`.
- `./scripts/prod.sh: Permission denied`: run `chmod +x scripts/*.sh` once.
