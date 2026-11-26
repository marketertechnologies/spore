# VM test for the secrets broker (docs/secrets.md): the agent user
# cannot read a secret by any path, but can run a profile command, and
# only root or a pilot can grant profiles or re-enable the fleet.
{ self, home-manager }:
{
  name = "spore-secrets-broker";
  nodes.machine = { config, pkgs, ... }: {
    imports = [
      self.nixosModules.spore-fleet
      home-manager.nixosModules.home-manager
    ];

    users.users.agent = { isNormalUser = true; linger = true; };
    users.users.pilot.isNormalUser = true;

    home-manager.useGlobalPkgs = true;
    home-manager.useUserPackages = true;
    home-manager.users.agent.home.stateVersion = config.system.stateVersion;

    environment.systemPackages = [ pkgs.git ];

    services.spore-fleet = {
      enable = true;
      user = "agent";
      projects.alpha.path = "/home/agent/alpha";
      claudeCodePackage = pkgs.writeShellScriptBin "claude" "exit 0";
      gracefulDeploy.enable = false;
      secrets = {
        enable = true;
        pilots = [ "pilot" ];
        projects.alpha.repo = "github.com/acme/alpha";
        projects.alpha.pushBranch = "^(wt|feature)/[A-Za-z0-9._-]+$";
        profiles.alpha-db = {
          project = "alpha";
          description = "read-only database";
          vars.DATABASE_URL = "DATABASE_URL_RO";
          commands.echo.argv = [ "${pkgs.bash}/bin/sh" "-c" "echo url=$DATABASE_URL" ];
          commands.env.argv = [ "env" ];
        };
      };
    };
  };

  testScript = { nodes, ... }:
    let
      spore = "${nodes.machine.services.spore-fleet.package}/bin/spore";
    in
    ''
      spore = "${spore}"
      machine.wait_for_unit("multi-user.target")
      machine.wait_for_unit("spore-secrets.socket")

      sd = "/var/lib/spore-secrets/secrets"
      machine.succeed(
          f"install -d -o spore-secrets -g spore-pilots -m 0750 {sd}/alpha",
          f"printf 'DATABASE_URL_RO=postgres://ro:canary-9f3a7c@db/app\\n' > {sd}/alpha/secrets.env",
          f"chown spore-secrets:spore-pilots {sd}/alpha/secrets.env",
          f"chmod 0640 {sd}/alpha/secrets.env",
          "install -d -o agent -m 0755 /home/agent/alpha",
          "runuser -u agent -- git -C /home/agent/alpha init -q",
      )

      def agent(cmd, task=""):
          env = "SPORE_PROJECT_ROOT=/home/agent/alpha"
          if task:
              env += f" SPORE_TASK_SLUG={task}"
          return f"cd /home/agent/alpha && runuser -u agent -- env {env} {cmd}"

      # No direct path to the value.
      machine.fail(agent(f"cat {sd}/alpha/secrets.env"))
      machine.fail(agent(f"ls {sd}"))
      machine.fail(agent("cat /etc/spore/secrets-broker.json | grep -q canary"))

      # The broker runs the profile command and redacts its output.
      out = machine.succeed(agent(f"{spore} secrets run alpha-db -- echo"))
      assert "canary" not in out, out
      assert "url=[redacted:DATABASE_URL_RO]" in out, out
      out = machine.succeed(agent(f"{spore} secrets run alpha-db -- env"))
      assert "canary" not in out and "DATABASE_URL=[redacted:DATABASE_URL_RO]" in out, out
      machine.fail(agent(f"{spore} secrets run alpha-db -- sh -c id"))
      machine.fail(agent(f"{spore} secrets run alpha-db -- echo extra"))
      pid = machine.succeed("systemctl show -p MainPID --value spore-secrets.service").strip()
      machine.fail(agent(f"cat /proc/{pid}/environ"))

      # Pilots read the files through the group; agents cannot grant.
      machine.succeed(f"runuser -u pilot -- cat {sd}/alpha/secrets.env | grep -q canary")
      machine.fail(agent(f"{spore} secrets grant t1 alpha-db --for 1h"))
      machine.succeed(f"runuser -u pilot -- {spore} secrets grant t1 alpha-db --for 1h --project alpha")
      machine.succeed(agent(f"{spore} secrets grants") + " | grep -q t1")
      machine.succeed(agent(f"{spore} secrets list", task="t1") + " | grep -q alpha-db")

      # The App token path is closed to agents.
      out = machine.fail(agent(f"{spore} gh token 2>&1"))
      assert "held by the secrets broker" in out, out

      # pr, api and push reach the broker, which applies its allowlist
      # before it needs a token.
      machine.succeed("grep -qF '\"push_branch\":\"^(wt|feature)/' /etc/spore/secrets-broker.json")
      out = machine.fail(agent(f"{spore} gh pr create --title t 2>&1"))
      assert "must be --draft" in out, out
      out = machine.fail(agent(f"{spore} gh pr merge 1 2>&1"))
      assert "pr merge is not allowed" in out, out
      out = machine.fail(agent(f"{spore} gh api repos/acme/other/pulls/1 2>&1"))
      assert "is not allowed" in out, out
      machine.succeed(
          "runuser -u agent -- git -C /home/agent/alpha -c user.name=t -c user.email=t@t commit -q --allow-empty -m base",
          "runuser -u agent -- git -C /home/agent/alpha branch -q hotfix/x",
      )
      out = machine.fail(agent(f"{spore} gh push hotfix/x 2>&1"))
      assert "push_branch" in out, out
      out = machine.fail(agent(f"{spore} gh push hotfix/x --force 2>&1"))
      assert "refused" in out, out

      # Kill switch: only root or a pilot re-enables; agents may stop.
      flag = "/var/lib/spore-fleet/fleet-enabled"
      machine.fail(agent(f"touch {flag}"))
      machine.fail(agent(f"SPORE_FLEET_FLAG={flag} {spore} fleet enable"))
      machine.fail(f"test -e {flag}")
      machine.succeed(f"SPORE_FLEET_FLAG={flag} {spore} fleet enable")
      machine.succeed(f"test -e {flag}")
      machine.succeed(agent(f"SPORE_FLEET_FLAG={flag} {spore} fleet disable"))
      machine.fail(f"test -e {flag}")
      machine.succeed(f"runuser -u pilot -- env SPORE_FLEET_FLAG={flag} {spore} fleet enable")
      machine.succeed(f"test -e {flag}")

      # Every call is logged, and the log carries no value.
      audit_log = "/var/lib/spore-secrets/state/audit.log"
      machine.succeed(f"grep -q '\"op\":\"run\"' {audit_log}")
      machine.succeed(f"grep -q '\"op\":\"grant\"' {audit_log}")
      machine.fail(f"grep -q canary {audit_log}")
      machine.fail(agent(f"cat {audit_log}"))
    '';
}
