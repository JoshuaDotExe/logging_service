# Deploying to OCI Always Free

This guide deploys the logging service to one Oracle Cloud (OCI) VM using
Terraform and GitHub Actions. Production supports the ARM-based A1 Flex shape.
All deployment runs from GitHub, not a laptop. The host runs Oracle Linux 9 for
optimal OCI integration and minimal resource footprint.

## How it fits together

```text
GitHub Actions                                   OCI (Always Free)
──────────────                                   ─────────────────
terraform.yml  ── Terraform (state in an OCI ─►  VCN, public subnet, NSGs
  (manual)        Object Storage bucket)         A1 Flex VM (Oracle Linux 9)
                                                 50 GB data volume → /srv/logging-service
                                                 Reserved public IP

build.yml (manual release tag)
  release → verify a published release and resolve its commit on main
  ci      → Go tests, shellcheck, Compose + nginx config validation
  images  → 6 arm64 images → ghcr.io/<owner>/<repo>/<service>:<sha>
  manifest → record image digests on the GitHub Release

deploy.yml (manual release tag)
  release → verify release, manifest and image availability
  deploy  → opens SSH for this runner's IP only, uploads a bundle,
            runs deploy/remote-deploy.sh, smoke-tests HTTPS, closes SSH
```

On the VM, Docker Compose runs `compose.yaml` plus
[`deploy/compose.prod.yaml`](../deploy/compose.prod.yaml):

- Only nginx publishes ports (80 and 443).
- PostgreSQL data lives on a separate block volume, so you can rebuild the VM
  without losing data.
- TLS comes from Let's Encrypt through HTTP-01 via nginx, with renewal checked
  daily by cron. Supply your own certificate to skip Let's Encrypt.
- Redis is not started: the deployed authoriser uses PostgreSQL. Nginx retains
  its per-IP rate limits, but API-key rate-limit settings are not enforced by
  this deployment.

| File | Purpose |
| --- | --- |
| [`infra/terraform/`](../infra/terraform) | Network, VM, data volume, reserved IP, cloud-init host bootstrap |
| [`.github/workflows/terraform.yml`](../.github/workflows/terraform.yml) | Validates on PRs; runs `plan` or `apply` on manual dispatch |
| [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) | Tests and deployment-config checks on PRs (reused by build) |
| [`.github/workflows/build.yml`](../.github/workflows/build.yml) | Runs CI and builds images for a published release |
| [`.github/workflows/deploy.yml`](../.github/workflows/deploy.yml) | Manually deploys previously built release images |
| [`deploy/build-bundle.sh`](../deploy/build-bundle.sh) | Runs in Actions; validates secrets and builds the release bundle |
| [`deploy/remote-deploy.sh`](../deploy/remote-deploy.sh) | Runs on the VM; installs secrets, migrates, starts services |
| [`deploy/tls.sh`](../deploy/tls.sh) | Runs on the VM; issues and renews the Let's Encrypt certificate |
| [`deploy/ssh-access.sh`](../deploy/ssh-access.sh) | Adds and removes the temporary SSH rule for the runner |

## 1. One-time OCI setup

Do this in the OCI Console or Cloud Shell. The names below are suggestions.
This guide uses a child compartment named `logging-service` for the state
bucket and all infrastructure. The IAM users and groups are created in the
tenancy's **Default** identity domain, not in the child compartment.

1. **Child compartment**: Identity & Security → Compartments → create
   `logging-service` under the tenancy's root compartment. Note its OCID; use
   it for `OCI_COMPARTMENT_OCID` below.
2. **State bucket**: Storage → Buckets, select the `logging-service`
   compartment, and create `logging-service-tfstate`. Set it to **Private** and
   enable **Object Versioning**. Get the namespace with `oci os ns get` in
   Cloud Shell, or from Tenancy details.
3. **Terraform user**:
   1. Go to Identity & Security → Domains, open the tenancy's **Default**
      domain, and use **User management** to create a regular group named
      `logging-service-terraform` and a user named `logging-service-terraform`.
      Add the user to the group. Do not create a dynamic group.
   2. Add an API key: Users → API keys → **Generate API key pair**.
   3. Download the private key. Note the fingerprint, user OCID and tenancy OCID
      shown in the configuration preview.
4. **Terraform policy**: create it in the tenancy's root compartment. The
   statements grant access to the child compartment and its state bucket:

   ```text
   Allow group logging-service-terraform to manage virtual-network-family in compartment logging-service
   Allow group logging-service-terraform to manage instance-family in compartment logging-service
   Allow group logging-service-terraform to manage volume-family in compartment logging-service
   Allow group logging-service-terraform to read buckets in compartment logging-service where target.bucket.name = 'logging-service-tfstate'
   Allow group logging-service-terraform to manage objects in compartment logging-service where target.bucket.name = 'logging-service-tfstate'
   ```

5. **Deployer user** (recommended): this gives the deploy workflow the least
   privilege it needs. In the same **Default** identity domain, create a regular
   group and user named `logging-service-deployer`, add the user to the group,
   and give the user its own API key. Create this policy in the tenancy's root
   compartment; it grants access only to the child compartment's network
   security groups:

   ```text
   Allow group logging-service-deployer to manage network-security-groups in compartment logging-service where any {request.permission = 'NETWORK_SECURITY_GROUP_UPDATE_SECURITY_RULES', request.permission = 'NETWORK_SECURITY_GROUP_LIST_SECURITY_RULES'}
   ```

   This lets the deployer only list, add and remove NSG rules. You could reuse
   the Terraform user's key instead, but then a leaked deploy credential could
   change all of the infrastructure.

## 2. Generate keys and passwords locally

```bash
# SSH key the deploy workflow uses to log in as the `deploy` user
ssh-keygen -t ed25519 -N '' -C github-actions-deploy -f deploy_ssh_key
#   deploy_ssh_key      -> secret   DEPLOY_SSH_PRIVATE_KEY
#   deploy_ssh_key.pub  -> variable DEPLOY_SSH_PUBLIC_KEY

openssl rand -hex 32                  # -> secret POSTGRES_PASSWORD
openssl rand -hex 32                  # -> secret TOKEN_SERVICE_KEY
openssl genpkey -algorithm ed25519    # -> secret JWT_PRIVATE_KEY (paste the whole PEM)
```

Paste multi-line PEM values exactly as printed, including the `-----BEGIN` and
`-----END` lines. Delete the local copies once they are stored, but keep an
offline backup of anything you cannot regenerate.

## 3. GitHub repository settings

1. **Settings → Environments**: create `infrastructure` and `production`. For
   both, set deployment branches to **Selected branches → `main`**. Add yourself
   as a **Required reviewer** on `infrastructure`, and optionally on
   `production`.
2. **Settings → Actions → General**:
   - For pull request workflows from forks, set approval to **Require approval
     for all external contributors**.
   - Set **Workflow permissions** to **Read repository contents and packages
     permissions**. The workflows request any extra scopes they need.
3. **Settings → Branches**: protect `main` by requiring pull requests and the CI
   checks.
4. **Packages**, after the first deploy: each image is linked to the repository
   by its `org.opencontainers.image.source` label. Private packages are fine;
   the deploy job pulls them with a short-lived `GITHUB_TOKEN`.

Store every secret below as an **environment secret**, never a repository
secret. Only jobs that pass the environment's protection rules can read
environment secrets, and pull request workflows never can.

### Environment `infrastructure` (used by `terraform.yml`)

| Kind | Name | Value |
| --- | --- | --- |
| Secret | `OCI_TENANCY_OCID` | Tenancy OCID |
| Secret | `OCI_USER_OCID` | Terraform user OCID |
| Secret | `OCI_FINGERPRINT` | Terraform user API key fingerprint |
| Secret | `OCI_PRIVATE_KEY` | Terraform user API private key (PEM) |
| Variable | `OCI_REGION` | Home region identifier, e.g. `uk-london-1`. Always Free compute must be in your home region |
| Variable | `OCI_COMPARTMENT_OCID` | OCID of the `logging-service` child compartment |
| Variable | `OCI_NAMESPACE` | Object Storage namespace |
| Variable | `TF_STATE_BUCKET` | `logging-service-tfstate` |
| Variable | `DEPLOY_SSH_PUBLIC_KEY` | Contents of `deploy_ssh_key.pub` |
| Variable (optional) | `ADMIN_SSH_PUBLIC_KEY` | Your own SSH public key for the `opc` user |
| Variable (optional) | `ADMIN_SSH_CIDRS` | JSON list of CIDRs allowed to SSH permanently, e.g. `["203.0.113.10/32"]` |
| Variable (optional) | `OCI_INSTANCE_SHAPE` | Must be `VM.Standard.A1.Flex` (the default) |
| Variable (optional) | `OCI_INSTANCE_OCPUS` | Default `2` |
| Variable (optional) | `OCI_INSTANCE_MEMORY_GBS` | Default `12` |
| Variable (optional) | `OCI_AVAILABILITY_DOMAIN_INDEX` | Default `0`. Try `1` or `2` on "Out of host capacity" |

### Environment `production` (used by `deploy.yml`)

| Kind | Name | Value |
| --- | --- | --- |
| Secret | `POSTGRES_PASSWORD` | `openssl rand -hex 32` |
| Secret | `TOKEN_SERVICE_KEY` | `openssl rand -hex 32` |
| Secret | `JWT_PRIVATE_KEY` | `openssl genpkey -algorithm ed25519` (PEM). The public key is derived automatically |
| Secret | `DEPLOY_SSH_PRIVATE_KEY` | Contents of `deploy_ssh_key` |
| Secret | `OCI_TENANCY_OCID` | Tenancy OCID |
| Secret | `OCI_USER_OCID` | Deployer user OCID |
| Secret | `OCI_FINGERPRINT` | Deployer API key fingerprint |
| Secret | `OCI_PRIVATE_KEY` | Deployer API private key (PEM) |
| Secret (optional) | `TLS_CERTIFICATE` | Full-chain PEM. Set it together with `TLS_PRIVATE_KEY` to skip Let's Encrypt |
| Secret (optional) | `TLS_PRIVATE_KEY` | Matching private key PEM |
| Variable | `DOMAIN` | Lowercase domain, e.g. `logs.example.com` |
| Variable | `ACME_EMAIL` | Let's Encrypt account email. Required unless the TLS secrets are set |
| Variable | `OCI_REGION` | Same as above |
| Variable | `DEPLOY_HOST` | Terraform output `public_ip` |
| Variable | `DEPLOY_SSH_KNOWN_HOSTS` | Terraform output `ssh_known_hosts` |
| Variable | `OCI_SSH_NSG_ID` | Terraform output `ssh_nsg_id` |
| Variable (optional) | `POSTGRES_USER`, `POSTGRES_DB` | Default `logging` for both |
| Variable (optional) | `JWT_ISSUER`, `JWT_AUDIENCE` | Defaults `logging-service` and `account-management` |

GitHub provides `GITHUB_TOKEN` automatically. The workflows use it to push and
pull images.

## 4. Provision the infrastructure

1. Go to **Actions → Terraform → Run workflow** and choose the `plan` action.
   Review the summary. It shows only resource addresses and actions; the full
   plan is never printed because the repository is public.
2. Run the workflow again with the `apply` action. If it fails with "Out of host
   capacity", retry later or consider a different availability domain.

   Changing the availability domain can require replacing the protected data
   volume if a previous partial apply created it. Review the plan before changing
   `OCI_AVAILABILITY_DOMAIN_INDEX`; preserve and migrate data rather than
   destroying the volume. If migrating an existing AMD VM, remove or change
   `OCI_INSTANCE_SHAPE` to `VM.Standard.A1.Flex`, then review the plan carefully:
   migration requires an ARM VM, may require replacement and downtime, and
   depends on A1 capacity. Preserve the data volume and backups. New releases
   built by this workflow cannot run on the old AMD VM.
3. Copy the three values from the run summary into the `production` environment
   variables: `DEPLOY_HOST`, `DEPLOY_SSH_KNOWN_HOSTS` and `OCI_SSH_NSG_ID`.
4. Create a DNS **A record** for `DOMAIN` that points at `DEPLOY_HOST`. The IP is
   reserved, so it survives instance rebuilds. Create the record **before the
   first deploy**, or certificate issuance will fail.

The VM bootstraps itself on first boot: it installs Docker, mounts the data
volume and applies hardening. This takes a few minutes, and the deploy job waits
for it.

## 5. Deploy

1. Merge changes into `main`. Deployments are not triggered by a push or merge.
2. On GitHub, open **Releases → Draft a new release**. Choose a new tag such as
   `v1.0.0`, target `main` (or an existing tag on `main`), add or generate release
   notes, and publish the release. A release is a version record, not a deployment.
3. Go to **Actions → Build release → Run workflow**, select `main`, enter the
   release tag in **release_version**, and run it. Wait for the build to succeed.
   The workflow runs CI, builds and publishes the six arm64 images on native ARM runners, and
   attaches `image-manifest.json` containing their digests to the release. The tag must
   contain only letters, digits, underscores, dots or hyphens, start with a
   letter, digit or underscore, and be at most 128 characters.
4. Go to **Actions → Deploy → Run workflow**, select `main`, enter the same
   release tag, and run it. Deployments and redeployments use the recorded
   digests; they do not rebuild images or rerun CI.

Both workflows accept published releases, including prereleases, only when the
tag points to a commit on `main` with digest-based deployment support. Releases
from before that support was merged cannot be built or deployed this way; publish
a new release from an updated `main`. Each workflow resolves the tag to a commit;
Deploy rejects a manifest whose release, repository or commit does not match.
The deployment workflow does the following:

1. Validates the release manifest and checks that all six images are available.
2. Builds the release bundle and opens port 22 to the runner's IP only.
3. Uploads the bundle as the `deploy` user, verifying the host key against
   Terraform's output.
4. On the VM:
   1. Writes `.env` and the keys.
   2. Pulls the images.
   3. Starts PostgreSQL.
   4. Runs the migrations.
   5. Starts the services and nginx.
   6. Obtains the certificate.
5. Smoke-tests `https://DOMAIN/health` and `/ping` and checks `/version` matches
   the selected release and commit, then always removes the SSH rule.

Pull-request CI does not build the six release service images;
those builds happen in **Build release**. CI still builds the nginx image to
validate its configuration and `/version` endpoint. If an image build fails, no
manifest is published; the release remains published and some images may already
have been pushed. Start a new build run or rerun **all** jobs, not just failed
jobs. A release with an existing manifest cannot be rebuilt: create a new release
instead. Preserve its manifest and GHCR image versions for future redeployments;
deleting either prevents deployment. Release administrators can replace assets,
so restrict release and package write access. Remove any required branch-protection
checks referring to the old `Build ...` matrix jobs.

To verify manually:

```bash
curl -fsS https://logs.example.com/health
curl -fsS https://logs.example.com/ping
curl -fsS https://logs.example.com/version
```

The public `/version` response is JSON, for example
`{"version":"v1.0.0","commit":"<full commit SHA>"}`. It reports the metadata
configured for the deployment, not the independent health of every container.
Local Compose runs report `development` for both fields unless overridden.
Check the Actions run for deployment success; publishing a release alone does
not mean it is deployed.

## Operations

- **Rotate `JWT_PRIVATE_KEY` or `TOKEN_SERVICE_KEY`**: update the secret, then run
  **Deploy** manually with the current release tag and **force_recreate** ticked. Existing JWTs stop
  validating.
- **Rotate `POSTGRES_PASSWORD`**: PostgreSQL reads the password only on first
  initialisation. Run `ALTER USER logging WITH PASSWORD '...'` on the host first,
  then update the secret and deploy with **force_recreate**.
- **Rebuild the VM**, for example after editing cloud-init: run **Terraform**
  with **replace_instance** ticked, `plan` first and then `apply`. The data
  volume, public IP and SSH host key are kept, so no GitHub variables change.
  Then run **Deploy**.
- **Roll back**: run **Deploy** with an earlier supported release tag only after
  checking that its code is compatible with the current database schema and
  data. Deploying an older release does not reverse database migrations.
- **Admin SSH**: set `ADMIN_SSH_PUBLIC_KEY` and `ADMIN_SSH_CIDRS`, then apply.
  Alternatively, use the OCI Console's serial console without opening port 22.
- **Logs**: on the host, run `docker logs <container>`, or use
  `docker compose -p logging-service logs -f` from `/opt/logging-service` with
  the same `-f` and `--env-file` flags that `remote-deploy.sh` uses.
- **Backups**: the data volume is not backed up automatically. Always Free
  includes five block volume backups. Schedule them in the Console, or add an
  `oci_core_volume_backup_policy_assignment`.
- **Idle reclamation**: Oracle may reclaim Always Free instances that stay idle
  for 7 days, meaning very low CPU, network and memory use. Upgrading the tenancy
  to Pay As You Go removes this risk while staying within the free limits.
- **Destroying**: the data volume has `prevent_destroy`. Remove it from
  [`storage.tf`](../infra/terraform/storage.tf) deliberately before running
  `terraform destroy`.

## Security notes

- Logs of public repositories are public. The workflows never print secrets, the
  Terraform plan or the bundle, and they pass secrets only through `env:`.
- Port 22 is closed except during a deploy, and then it is open only to the
  runner's IP. `sshd` accepts only keys, only for the `deploy` and `opc`
  users, and only the ed25519 host key that Terraform generated.
- The Terraform state contains the SSH host private key, so the state bucket
  must stay private.
- The application-level findings from the security review still apply and should
  be fixed before real use:
  - Log reads and deletes are not scoped to the account.
  - API keys stay valid after an account is suspended.
