#!/usr/bin/env bash
# badal-forge — NVIDIA driver + Secure Boot MOK enrollment (Fedora Server)
#
# WHY THIS EXISTS
#   Under Secure Boot, akmod-nvidia rebuilds the kernel module on every kernel
#   update. A freshly built module is UNSIGNED, and Secure Boot refuses to load
#   unsigned modules. The box then boots perfectly, nvidia-smi fails, and every
#   GPU job dies silently. On a headless machine you will not notice for days.
#
#   Enrolling a Machine Owner Key (MOK) once, and configuring akmods to sign
#   automatically, prevents this permanently.
#
# RUN THIS WHILE THE LAPTOP IS PHYSICALLY IN FRONT OF YOU.
#   Step 2 requires an interactive reboot: a blue "MOK Manager" screen appears
#   BEFORE the OS starts and cannot be driven over SSH. Miss it and the key is
#   not enrolled.
#
# Usage:  sudo ./00-nvidia-mok.sh install    # then reboot and enroll
#         sudo ./00-nvidia-mok.sh verify     # after the reboot
set -euo pipefail

FEDORA_VER="$(rpm -E %fedora)"
CERT=/etc/pki/akmods/certs/public_key.der

need_root() { [ "$EUID" -eq 0 ] || { echo "run as root"; exit 1; }; }

cmd_install() {
  need_root
  echo "== Secure Boot state =="
  local sb; sb="$(mokutil --sb-state 2>/dev/null || echo 'unknown')"
  echo "  $sb"

  echo "== RPMFusion repos =="
  dnf -y install \
    "https://mirrors.rpmfusion.org/free/fedora/rpmfusion-free-release-${FEDORA_VER}.noarch.rpm" \
    "https://mirrors.rpmfusion.org/nonfree/fedora/rpmfusion-nonfree-release-${FEDORA_VER}.noarch.rpm" \
    || true

  echo "== driver + signing tooling =="
  # akmod-nvidia covers the GTX 1650 (Turing TU117) on current drivers.
  dnf -y install akmod-nvidia xorg-x11-drv-nvidia-cuda kmodtool akmods mokutil openssl

  if echo "$sb" | grep -qi 'enabled'; then
    echo "== generating MOK (Secure Boot is ON) =="
    [ -f "$CERT" ] || kmodgenca -a
    echo
    echo "  You will now be asked to set a ONE-TIME password."
    echo "  You must retype it at the blue MOK Manager screen on the next boot."
    echo
    mokutil --import "$CERT"
    echo
    echo "  NEXT: reboot with a display attached."
    echo "     -> 'Perform MOK management' -> 'Enroll MOK' -> 'Continue' -> 'Yes'"
    echo "     -> enter the one-time password -> reboot"
    echo "  Then run:  sudo $0 verify"
  else
    echo "== Secure Boot is OFF — no MOK needed =="
    echo "   The driver will load unsigned. If you ever enable Secure Boot,"
    echo "   re-run this script or the GPU goes dark after the next kernel update."
  fi

  echo "== pinning the kernel out of unattended updates =="
  # A kernel update that outruns a signed module rebuild is the exact failure
  # this script prevents. Update kernels deliberately, with console access.
  if ! grep -q '^exclude=kernel' /etc/dnf/dnf.conf 2>/dev/null; then
    echo 'exclude=kernel* kernel-core* kernel-modules*' >> /etc/dnf/dnf.conf
  fi
  echo "  /etc/dnf/dnf.conf: $(grep '^exclude' /etc/dnf/dnf.conf)"
  echo
  echo "  To update the kernel later, on purpose:"
  echo "    dnf --disableexcludes=all update kernel\\* && reboot   # then: $0 verify"
}

cmd_verify() {
  need_root
  local ok=0
  echo "== MOK enrolled? =="
  if mokutil --sb-state 2>/dev/null | grep -qi 'enabled'; then
    mokutil --test-key "$CERT" 2>&1 | sed 's/^/  /'
    mokutil --test-key "$CERT" 2>/dev/null | grep -qi 'already enrolled' || ok=1
  else
    echo "  Secure Boot off — skipping"
  fi

  echo "== module signature =="
  modinfo -F signer nvidia 2>/dev/null | sed 's/^/  /' || { echo "  nvidia module NOT loadable"; ok=1; }

  echo "== nvidia-smi =="
  if nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader 2>/dev/null | sed 's/^/  /'; then :; else
    echo "  FAILED"; ok=1
  fi

  echo
  [ "$ok" -eq 0 ] && echo "RESULT: GPU stack healthy" || echo "RESULT: BROKEN — do not enable Secure Boot kernel updates until fixed"
  return "$ok"
}

# Post-boot guard: report GPU-incapable and alert instead of accepting jobs
# the node cannot actually run. Installed by the forge Ansible role.
cmd_install_watchdog() {
  need_root
  cat > /usr/local/bin/badal-gpu-check <<'EOS'
#!/usr/bin/env bash
# Reports GPU health to badal-api; alerts via ntfy if the driver vanished.
if nvidia-smi -L >/dev/null 2>&1; then
  exit 0
fi
logger -t badal "GPU UNAVAILABLE: nvidia-smi failed (likely unsigned module after kernel update)"
[ -n "${NTFY_URL:-}" ] && curl -fsS -H "Title: badal-forge GPU down" \
  -d "nvidia-smi failed. Module signing likely broke after a kernel update." "$NTFY_URL" >/dev/null 2>&1
exit 1
EOS
  chmod +x /usr/local/bin/badal-gpu-check
  cat > /etc/systemd/system/badal-gpu-check.service <<'EOS'
[Unit]
Description=badal GPU health check
After=multi-user.target
[Service]
Type=oneshot
EnvironmentFile=-/etc/badal/forge.env
ExecStart=/usr/local/bin/badal-gpu-check
EOS
  cat > /etc/systemd/system/badal-gpu-check.timer <<'EOS'
[Unit]
Description=badal GPU health check (boot + hourly)
[Timer]
OnBootSec=2min
OnUnitActiveSec=1h
[Install]
WantedBy=timers.target
EOS
  systemctl daemon-reload
  systemctl enable --now badal-gpu-check.timer
  echo "[ok] GPU watchdog installed"
}

case "${1:-}" in
  install)          cmd_install ;;
  verify)           cmd_verify ;;
  install-watchdog) cmd_install_watchdog ;;
  *) echo "usage: $0 {install|verify|install-watchdog}"; exit 1 ;;
esac
