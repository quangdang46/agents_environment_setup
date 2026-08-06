# Hetzner Cloud VPS Setup Guide

Set up a VPS on Hetzner Cloud for running ACFS and coding agents.

---

## Provider Overview

**Hetzner** is a German hosting provider with excellent performance, modern UI, and competitive European pricing.

| Aspect | Details |
|--------|---------|
| **Recommended Tier** | CX21 or CX31 (~$5-10/mo) |
| **Minimum Specs** | 2 vCPU, 4GB RAM, 40GB SSD |
| **Best For** | Developers who want a modern cloud experience |
| **Signup** | [hetzner.com/cloud](https://www.hetzner.com/cloud/) |

### Pros
- Excellent price-to-performance ratio
- Beautiful, modern control panel
- Fast provisioning (< 1 minute)
- Great API and CLI tools
- Terraform support

### Cons
- Fewer global locations (EU + US only)
- Requires identity verification for new accounts

---

## Step 1: Create an Account

1. Go to [accounts.hetzner.com](https://accounts.hetzner.com)
2. Click "Register" and create an account
3. Complete identity verification (may require ID upload)

![Hetzner Step 1: Create account](screenshots/hetzner-step1-create-account.png)

---

## Step 2: Access Hetzner Cloud Console

1. Log into [console.hetzner.cloud](https://console.hetzner.cloud)
2. Create a new project (if first time)
3. Click "Add Server"

![Hetzner Step 2: Cloud console](screenshots/hetzner-step2-console.png)

---

## Step 3: Choose Location

Select a data center:
- **Europe**: Nuremberg, Falkenstein, Helsinki
- **Americas**: Ashburn (Virginia), Hillsboro (Oregon)

Pick the closest location to you for best latency.

![Hetzner Step 3: Select location](screenshots/hetzner-step3-select-location.png)

---

## Step 4: Select Operating System

1. Under "Image", click "Ubuntu"
2. Select **Ubuntu 24.04**

![Hetzner Step 4: Select Ubuntu](screenshots/hetzner-step4-select-os.png)

---

## Step 5: Choose Server Type

Hetzner offers shared and dedicated CPU options:

**Shared vCPU (Recommended for ACFS)**:
| Type | vCPU | RAM | Storage | Price |
|------|------|-----|---------|-------|
| CX21 | 2 | 4GB | 40GB | ~$5/mo |
| CX31 | 2 | 8GB | 80GB | ~$8/mo |
| CX41 | 4 | 16GB | 160GB | ~$15/mo |

**Recommended**: CX21 or CX31

![Hetzner Step 5: Select type](screenshots/hetzner-step5-select-type.png)

---

## Step 6: Add Your SSH Key

This is the recommended way to access your server.

1. Click "Add SSH Key"
2. Paste your public SSH key
3. Give it a name

If you don't have an SSH key yet:
```bash
ssh-keygen -t ed25519 -C "your-email@example.com"
cat ~/.ssh/id_ed25519.pub
```

![Hetzner Step 6: Add SSH key](screenshots/hetzner-step6-add-ssh-key.png)

---

## Step 7: Configure Networking

Leave defaults:
- **Public IPv4**: Enabled
- **Public IPv6**: Enabled
- **Private Network**: Optional

![Hetzner Step 7: Networking](screenshots/hetzner-step7-networking.png)

---

## Step 8: Name Your Server

1. Enter a memorable name (e.g., "acfs-dev")
2. Review configuration
3. Click "Create & Buy now"

Your server will be ready in under 1 minute!

![Hetzner Step 8: Create server](screenshots/hetzner-step8-create-server.png)

---

## Step 9: Find Your IP Address

Once created:

1. Click on your server in the dashboard
2. Copy the **IPv4 address** from the overview

![Hetzner Step 9: Find IP](screenshots/hetzner-step9-find-ip.png)

---

## Step 10: Connect via SSH

```bash
ssh root@YOUR_IP_ADDRESS
```

Hetzner uses `root` by default with SSH key authentication.

---

## Step 11: Create Ubuntu User (Recommended)

ACFS expects an `ubuntu` user. Create it:

```bash
# Create user with sudo
adduser ubuntu
usermod -aG sudo ubuntu

# Set up SSH for the new user
mkdir -p /home/ubuntu/.ssh
cp ~/.ssh/authorized_keys /home/ubuntu/.ssh/
chown -R ubuntu:ubuntu /home/ubuntu/.ssh
chmod 700 /home/ubuntu/.ssh
chmod 600 /home/ubuntu/.ssh/authorized_keys

# Enable passwordless sudo
echo "ubuntu ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers.d/ubuntu
```

Now reconnect as ubuntu:
```bash
exit
ssh ubuntu@YOUR_IP_ADDRESS
```

---

## Hetzner-Specific Notes

### Cloud-Init Template
For automated server bootstrap, use the companion cloud-init template:

```bash
hcloud server create \
  --name acfs-dev \
  --type cpx31 \
  --image ubuntu-24.04 \
  --ssh-key your-key-name \
  --user-data-from-file scripts/providers/hetzner-cloud-init.yml
```

### Default User
Hetzner uses `root` by default. Create the `ubuntu` user manually (see Step 11).

### Firewall
Hetzner Cloud has a built-in firewall feature (free). Consider creating rules:
1. Go to "Firewalls" in the sidebar
2. Create a new firewall
3. Allow SSH (port 22)
4. Apply to your server

### CLI Tool
Hetzner has an excellent CLI:
```bash
# Install
brew install hcloud  # macOS
# or
curl -o hcloud.tar.gz -L https://github.com/hetznercloud/cli/releases/latest/download/hcloud-linux-amd64.tar.gz

# Use
hcloud server list
hcloud server ssh my-server
```

### Snapshots
Take snapshots before major changes:
- Go to server > Snapshots
- Click "Take Snapshot"
- Can restore anytime

### Support
- Documentation: [docs.hetzner.com](https://docs.hetzner.com)
- Community: [community.hetzner.com](https://community.hetzner.com)

---

## Next Step

Once connected as `ubuntu`, run the ACFS installer:

```bash
curl -fsSL https://raw.githubusercontent.com/quangdang46/agents_environment_setup/main/install.sh | bash
```

---

*Screenshots are placeholders. Replace with actual screenshots from Hetzner Cloud console.*
