# GitHub App Setup for Argus

Argus supports GitHub App authentication as an alternative to Personal Access Tokens (PATs).
A GitHub App provides fine-grained permissions, automatic token refresh, and a smoother
onboarding experience for organizations.

## Why GitHub App over PAT?

| Feature | PAT | GitHub App |
|---|---|---|
| Permissions | All-or-nothing (user scope) | Fine-grained (repo-level) |
| Token lifetime | No expiry (until revoked) | 1 hour (auto-refreshed) |
| Rate limit | 5,000 req/h (user) | 15,000 req/h (installation) |
| Audit trail | Tied to personal account | Independent bot identity |
| Installation | Manual per-repo setup | One-click org/repo install |

## Step 1: Create the GitHub App

### Option A: Using the Manifest Flow (Recommended)

GitHub's manifest flow pre-fills all required settings. You provide a JSON manifest,
GitHub creates the App, and you receive the App ID and private key.

1. Go to **Settings > Developer settings > GitHub Apps** on github.com (or your
   GitHub Enterprise instance).

2. Click **New GitHub App** and select **Create a GitHub App from a manifest**.

3. Provide a manifest URL or paste the JSON. You can use the template at
   `deploy/github-app-manifest.json` in this repository. Update these fields first:

   ```json
   {
     "name": "Argus Code Review",
     "hook_attributes": {
       "url": "https://YOUR-ARGUS-HOST/webhook/github"
     },
     "redirect_url": "https://YOUR-ARGUS-HOST/callback",
     "setup_url": "https://YOUR-ARGUS-HOST/setup"
   }
   ```

4. Review the pre-filled permissions and events, then click **Create GitHub App**.

5. GitHub will redirect you to the App's settings page. Note the **App ID** shown
   at the top.

### Option B: Manual Setup

1. Go to **Settings > Developer settings > GitHub Apps > New GitHub App**.

2. Fill in the basic information:
   - **GitHub App name**: `Argus Code Review` (or your preferred name)
   - **Description**: `AI-powered code review bot`
   - **Homepage URL**: Your Argus instance URL

3. Set the **Webhook URL**:
   ```
   https://YOUR-ARGUS-HOST/webhook/github
   ```

4. Set a **Webhook secret** (generate a random string; you'll need it for
   `ARGUS_GITHUB_SECRET`):
   ```bash
   openssl rand -hex 20
   ```

5. Configure **Repository permissions**:
   - Pull requests: **Read & write**
   - Issues: **Read & write**
   - Contents: **Read-only**
   - Metadata: **Read-only** (auto-selected)

6. Subscribe to **events**:
   - Pull request
   - Pull request review
   - Issue comment
   - Push
   - Installation
   - Installation repositories

7. Under **Where can this GitHub App be installed?**, choose:
   - **Only on this account** (for personal/org use)
   - **Any account** (for public distribution)

8. Click **Create GitHub App**.

## Step 2: Generate a Private Key

1. On the App's settings page, scroll to **Private keys**.
2. Click **Generate a private key**.
3. A `.pem` file will be downloaded. Store it securely.

## Step 3: Install the App

1. On the App's settings page, click **Install App** in the left sidebar.
2. Select the organization or account.
3. Choose which repositories to grant access to:
   - **All repositories** — Argus can review any repo in the org
   - **Only select repositories** — Pick specific repos
4. Click **Install**.

Note the **Installation ID** from the URL after installation:
```
https://github.com/settings/installations/12345678
                               Installation ID: ^^^^^^^^
```

## Step 4: Configure Argus

Set these environment variables (or add to your `.env` / K8s secret):

```bash
# Required: GitHub App credentials
export ARGUS_GITHUB_APP_ID=123456
export ARGUS_GITHUB_APP_PRIVATE_KEY="$(cat argus-app.pem)"
export ARGUS_GITHUB_APP_INSTALLATION_ID=12345678

# Required: webhook signature verification
export ARGUS_GITHUB_SECRET="your-webhook-secret"

# Optional: GitHub Enterprise URL (default: https://github.com)
export ARGUS_GITHUB_URL="https://github.example.com"
```

### K8s Deployment

For K8s, encode the private key and add it to your secret:

```bash
# Base64-encode the private key
PRIVATE_KEY_B64=$(base64 -i argus-app.pem | tr -d '\n')

# Update the K8s secret
kubectl -n argus patch secret argus-secret --type='json' -p='[
  {"op":"replace","path":"/data/ARGUS_GITHUB_APP_ID","value":"'$(echo -n 123456 | base64)'"},
  {"op":"replace","path":"/data/ARGUS_GITHUB_APP_PRIVATE_KEY","value":"'"$PRIVATE_KEY_B64"'"},
  {"op":"replace","path":"/data/ARGUS_GITHUB_APP_INSTALLATION_ID","value":"'$(echo -n 12345678 | base64)'"}
]'
```

Or edit `deploy/k8s/01-secret.yaml` directly and apply.

### Docker Compose

Add to your `.env` or `docker-compose.yml` environment:

```yaml
environment:
  ARGUS_GITHUB_APP_ID: "123456"
  ARGUS_GITHUB_APP_PRIVATE_KEY: |
    -----BEGIN RSA PRIVATE KEY-----
    MIIEpAIBAAKCAQEA...
    -----END RSA PRIVATE KEY-----
  ARGUS_GITHUB_APP_INSTALLATION_ID: "12345678"
  ARGUS_GITHUB_SECRET: "your-webhook-secret"
```

## Step 5: Verify

1. Restart Argus. You should see in the logs:
   ```
   argus.githubapp.token_ready  app_id=123456 installation_id=12345678 expires_at=...
   argus.sdkctx.platform_ready  platform=github auth=github_app
   ```

2. Open a PR in an installed repository. Argus should receive the webhook and
   start a review task.

3. The token refreshes automatically every 50 minutes. You'll see:
   ```
   argus.githubapp.token_refreshed  expires_at=...
   ```

## How It Works

1. **Startup**: Argus reads `ARGUS_GITHUB_APP_ID`, `ARGUS_GITHUB_APP_PRIVATE_KEY`,
   and `ARGUS_GITHUB_APP_INSTALLATION_ID`. It signs a JWT with the private key
   (RS256, standard library only), exchanges it for an installation access token
   via the GitHub API, and creates the SDK provider with that token.

2. **Token refresh**: A background goroutine refreshes the token every 50 minutes
   (tokens expire after 1 hour). The provider is rebuilt transparently; in-flight
   requests use the old token (still valid) and new requests use the refreshed token.

3. **Webhook events**: When GitHub sends `installation` or
   `installation_repositories` events, Argus logs the installation details
   (account, repos) and acknowledges the event. This enables observability into
   which repos are connected.

4. **Workcopy credentials**: When Argus clones repos for analysis, it uses the
   current installation token (auto-refreshed) instead of a static PAT.

## Permissions Explained

| Permission | Level | Why Argus Needs It |
|---|---|---|
| Pull requests | Read & write | Read PR diffs; post review comments |
| Issues | Read & write | Read linked issues; post summary comments |
| Contents | Read-only | Read `.reviewagents.yaml` config from default branch |
| Metadata | Read-only | Required by GitHub; auto-granted |

## Troubleshooting

### "initial token fetch failed"

- Verify `ARGUS_GITHUB_APP_ID` matches the App ID on the settings page.
- Verify `ARGUS_GITHUB_APP_PRIVATE_KEY` contains the full PEM (including
  `-----BEGIN/END-----` lines). Base64-encoded PEM is also accepted.
- Verify `ARGUS_GITHUB_APP_INSTALLATION_ID` matches the installation ID
  (from the URL after installing the App).

### "GitHub API returned 404"

- The installation ID is wrong or the App has been uninstalled.
- Re-install the App and note the new installation ID.

### "GitHub API returned 401"

- The private key doesn't match the App. Regenerate a key on the App settings page.
- The JWT clock may be skewed. Argus uses a 10-second `iat` tolerance.

### "GitHub API returned 403"

- The installation doesn't have access to the requested repository.
- Go to the App's installation settings and add the repository.

### PAT still being used

If `ARGUS_GITHUB_TOKEN` is also set, GitHub App takes precedence only when all three
App fields are configured. If any field is missing, Argus falls back to PAT.

## Migration from PAT

1. Create and install the GitHub App (Steps 1-3 above).
2. Set the three `ARGUS_GITHUB_APP_*` environment variables.
3. You can keep `ARGUS_GITHUB_TOKEN` as a fallback, or remove it.
4. Restart Argus. The log will show `auth=github_app` instead of PAT-based auth.
5. Update the webhook secret: GitHub App uses its own webhook secret configured
   during App creation. Set `ARGUS_GITHUB_SECRET` to match.
