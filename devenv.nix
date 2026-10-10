{ pkgs, ... }:

{
  # https://devenv.sh/packages/
  packages = [
    pkgs.git
    pkgs.golangci-lint
    pkgs.just
  ];

  # Go 1.27 is required for generic methods.
  languages.go.enable = true;
  languages.go.package = pkgs.go_1_27;

  # https://devenv.sh/processes/
  # processes.dev.exec = "${lib.getExe pkgs.watchexec} -n -- ls -la";

  # https://devenv.sh/scripts/
  # https://devenv.sh/basics/
  enterShell = ''
    git --version
  '';

  # https://devenv.sh/tests/
  enterTest = ''
    git --version | grep "${pkgs.git.version}"
  '';
}
