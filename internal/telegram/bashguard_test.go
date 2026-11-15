package telegram

import (
	"testing"
)

func TestCheckBashCommandSafe(t *testing.T) {
	safe := []string{
		"df -h",
		"systemctl status nginx",
		"journalctl -u nginx --since today",
		"ls -la /var/log",
		"grep -r error /var/log/nginx/",
		"du -sh /var",
		"free -m",
		"rm -rf build-cache",
		"rm -r ./build",
		"rm old-file.txt",
		"uptime",
	}
	for _, cmd := range safe {
		if reason, bad := CheckBashCommand(cmd); bad {
			t.Errorf("%q refused: %s", cmd, reason)
		}
	}
}

func TestCheckBashCommandDangerous(t *testing.T) {
	dangerous := []string{
		"sudo rm -rf /",
		"sudo apt install evil",
		"shutdown -h now",
		"reboot",
		"poweroff",
		"init 6",
		"mkfs.ext4 /dev/sda1",
		"dd if=/dev/zero of=/dev/sda",
		"iptables -F",
		"ufw disable",
		"nft flush ruleset",
		"echo hack > /etc/ssh/sshd_config",
		"cat ~/.ssh/authorized_keys",
		"useradd backdoor",
		"passwd root",
		"visudo",
		"chmod -R 777 /",
		"chown -R attacker /",
		":(){ :|:& };:",
		"rm -rf /",
		"rm -rf /*",
		"rm -rf ~",
		"rm -rf $HOME",
		"rm -rf /etc",
		"rm -r /var/log/*",
		"rm -rf /tmp/anything-absolute",
		"ls; sudo reboot",
	}
	for _, cmd := range dangerous {
		if _, bad := CheckBashCommand(cmd); !bad {
			t.Errorf("%q was NOT refused", cmd)
		}
	}
}

func TestCheckBashCommandEmpty(t *testing.T) {
	if _, bad := CheckBashCommand("   "); !bad {
		t.Fatal("empty command should be refused")
	}
}
