# SSH Troubleshooting Guide

This guide covers common SSH connection issues when connecting to your VPS.

---

## Quick Diagnosis

Run this command with verbose output to see exactly what's happening:

```bash
ssh -v -i ~/.ssh/id_ed25519 ubuntu@YOUR_SERVER_IP
```

The `-v` flag shows detailed connection steps that help identify the problem.

---

## Problem: Permission denied (publickey)

**What you see:**
```
Permission denied (publickey).
```

**Common causes:**

### 1. Wrong key file
Your SSH key might not be where you think it is.

**Check your keys:**
```bash
ls -la ~/.ssh/
```

You should see `id_ed25519` and `id_ed25519.pub` (or similar).

**Fix:** Specify the correct key:
```bash
ssh -i ~/.ssh/id_ed25519 ubuntu@YOUR_SERVER_IP
```

### 2. Key not added to VPS
Your public key wasn't added during VPS setup.

**Fix options:**
- **If you have console access:** Log into your VPS provider's web console, then add your key manually:
  ```bash
  if [ -L ~/.ssh ] || [ -L ~/.ssh/authorized_keys ]; then
    echo "Refusing to write through a symlinked SSH path" >&2
  else
    mkdir -p ~/.ssh
    chmod 700 ~/.ssh
    touch ~/.ssh/authorized_keys
    printf 'Paste your public key, then press Enter: '
    IFS= read -r pubkey
    if [ -n "$pubkey" ]; then
      { [ ! -s ~/.ssh/authorized_keys ] || tail -c 1 ~/.ssh/authorized_keys | od -An -t u1 | grep -qw 10 || printf '\n' >> ~/.ssh/authorized_keys; }
      grep -qxF "$pubkey" ~/.ssh/authorized_keys || printf '%s\n' "$pubkey" >> ~/.ssh/authorized_keys
      chmod 600 ~/.ssh/authorized_keys
    fi
  fi
  ```
- **If creating a new VPS:** Make sure to paste your public key (the `.pub` file contents) during setup.

**Get your public key:**
```bash
cat ~/.ssh/id_ed25519.pub
```

### 3. Wrong username
Different VPS providers use different default usernames.

| Provider | Default Username |
|----------|-----------------|
| OVH | `ubuntu` |
| Contabo | `root` |

**Fix:** Try the correct username:
```bash
ssh root@YOUR_SERVER_IP     # For most providers
ssh ubuntu@YOUR_SERVER_IP   # For OVH with Ubuntu
```

### 4. Key permissions too open
SSH refuses keys with insecure permissions.

**Fix:**
```bash
chmod 600 ~/.ssh/id_ed25519
chmod 644 ~/.ssh/id_ed25519.pub
chmod 700 ~/.ssh
```

---

## Problem: Connection refused

**What you see:**
```
ssh: connect to host X.X.X.X port 22: Connection refused
```

**Common causes:**

### 1. SSH service not running
The SSH daemon might not be running on the VPS.

**Fix (if you have console access):**
```bash
sudo systemctl start ssh
sudo systemctl enable ssh
```

### 2. Firewall blocking port 22
The VPS or your network might be blocking SSH.

**Check with your VPS provider:**
- Look for "Firewall" or "Security Groups" in their dashboard
- Ensure port 22 (SSH) is allowed for inbound traffic
- Some providers have this enabled by default, others don't

### 3. Wrong IP address
Double-check the IP address in your provider's dashboard.

**Verify the IP is reachable:**
```bash
ping YOUR_SERVER_IP
```

If ping works but SSH doesn't, it's likely a firewall issue.

---

## Problem: Connection timed out

**What you see:**
```
ssh: connect to host X.X.X.X port 22: Connection timed out
```

**Common causes:**

### 1. VPS not fully booted
Fresh VPS can take 1-3 minutes to become accessible.

**Fix:** Wait 2-3 minutes after VPS creation, then try again.

### 2. Wrong IP address
The IP you're using might be incorrect or from an old VPS.

**Fix:** Verify the IP in your provider's dashboard. It should match exactly.

### 3. Network issues
There might be a network problem between you and the VPS.

**Diagnose:**
```bash
# Check if the IP is reachable at all
ping YOUR_SERVER_IP

# Check the route to the server
traceroute YOUR_SERVER_IP   # Mac/Linux
tracert YOUR_SERVER_IP      # Windows
```

### 4. Corporate firewall
Some corporate networks block outgoing SSH (port 22).

**Fix options:**
- Try from a different network (home, mobile hotspot)
- Ask IT to allow outgoing port 22
- Some VPS providers offer SSH on alternate ports

---

## Problem: Host key verification failed

**What you see:**
```
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
...
Host key verification failed.
```

**Why this happens:**
- You recreated your VPS but kept the same IP
- The VPS was reinstalled with a new OS
- (Rarely) Someone is intercepting your connection

**If you just recreated your VPS (the common case):**

```bash
# Remove the old host key
ssh-keygen -R YOUR_SERVER_IP

# Then connect again - you'll be prompted to accept the new key
ssh ubuntu@YOUR_SERVER_IP
```

**On Windows (PowerShell):**
```powershell
# Open the known_hosts file and remove the line with your IP
notepad $env:USERPROFILE\.ssh\known_hosts
```

---

## Problem: Too many authentication failures

**What you see:**
```
Received disconnect from X.X.X.X port 22: Too many authentication failures
```

**Why this happens:**
You have multiple SSH keys and the client tries them all before finding the right one.

**Fix:** Specify the exact key to use:
```bash
ssh -i ~/.ssh/id_ed25519 -o IdentitiesOnly=yes ubuntu@YOUR_SERVER_IP
```

---

## Problem: Slow connection / Hangs on login

**What you see:**
Connection works but takes 30+ seconds, or hangs after "debug1: Authentications that can continue"

**Common causes:**

### 1. DNS reverse lookup
The server is trying to look up your IP address.

**Fix (on the server, if you can access it):**
```bash
sudo nano /etc/ssh/sshd_config
# Add or change:
UseDNS no
# Then restart SSH:
sudo systemctl restart ssh
```

### 2. GSSAPI authentication delays
Kerberos authentication is timing out.

**Fix (client-side):**
```bash
ssh -o GSSAPIAuthentication=no ubuntu@YOUR_SERVER_IP
```

---

## Still stuck?

### Collect debug info
Run with maximum verbosity:
```bash
ssh -vvv -i ~/.ssh/id_ed25519 ubuntu@YOUR_SERVER_IP 2>&1 | tee ssh_debug.log
```

### Check VPS provider console
Most providers offer a web-based console that bypasses SSH entirely. Use it to:
1. Verify the server is running
2. Check network configuration
3. Restart SSH service
4. Add your public key manually

### Verify your public key format
Your public key should look like:
```
ssh-ed25519 AAAAC3NzaC1lZDI1... your_email@example.com
```

Make sure there are no extra line breaks when you paste it.

---

## Prevention: SSH Config File

Create `~/.ssh/config` to save your connection settings:

```
Host myserver
    HostName YOUR_SERVER_IP
    User ubuntu
    IdentityFile ~/.ssh/id_ed25519
    IdentitiesOnly yes
```

Then connect with just:
```bash
ssh myserver
```
