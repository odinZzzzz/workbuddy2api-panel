import os, sys, time, paramiko

HOST="ct-game.com"; USER="root"; PW=os.environ["SSHPW"]
LOCAL=r"C:\Users\Admin\Documents\deepseek-harness\default-workspace\workbuddy2api-panel\wb2api-linux"
REMOTE="/opt/workbuddy2api/wb2api"

def run(c, cmd, timeout=60):
    i,o,e = c.exec_command(cmd, timeout=timeout)
    out = o.read().decode(errors="replace"); err = e.read().decode(errors="replace")
    rc = o.channel.recv_exit_status()
    return rc, out, err

c = paramiko.SSHClient(); c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
c.connect(HOST, 22, USER, PW, timeout=30, banner_timeout=30, auth_timeout=30)
print("[1] connected")

rc,out,err = run(c, "cp -f /opt/workbuddy2api/wb2api /opt/workbuddy2api/wb2api.bak-$(date +%H%M%S) && ls -1 /opt/workbuddy2api/wb2api.bak-* | tail -3")
print("[2] backup rc=%d\n%s%s" % (rc, out, err))
if rc != 0:
    print("BACKUP FAILED"); sys.exit(2)

sftp = c.open_sftp()
sftp.put(LOCAL, "/opt/workbuddy2api/wb2api.new")
print("[3] uploaded")
sftp.close()

rc,out,err = run(c, "chmod +x /opt/workbuddy2api/wb2api.new && ls -l /opt/workbuddy2api/wb2api.new && md5sum /opt/workbuddy2api/wb2api.new")
print("[4] chmod rc=%d\n%s%s" % (rc, out, err))

rc,out,err = run(c, "mv -f /opt/workbuddy2api/wb2api.new /opt/workbuddy2api/wb2api && systemctl restart workbuddy2api")
print("[5] install+restart rc=%d %s%s" % (rc, out, err))

time.sleep(3)
rc,out,err = run(c, "systemctl is-active workbuddy2api; systemctl status workbuddy2api --no-pager -n 12")
print("[6] status rc=%d\n%s%s" % (rc, out, err))

rc,out,err = run(c, "curl -s -m 10 http://127.0.0.1:7863/healthz")
print("[7] healthz rc=%d body=%r %s" % (rc, out.strip(), err))

c.close()
print("DONE")
