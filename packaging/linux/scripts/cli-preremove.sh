#!/bin/sh
# Package scripts run as root and cannot manage each user's systemd --user
# service, so only remind. Skipped on upgrade (deb "upgrade", rpm $1 = 1).
case "$1" in
upgrade | 1) exit 0 ;;
esac
echo "e5extend: if you installed the background service, run 'e5extend service uninstall' as that user;" >&2
echo "e5extend: otherwise the service keeps failing because the program is gone. Your config and data are kept." >&2
exit 0
