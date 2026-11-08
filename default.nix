{
  pkgs,
  go,
}: let
  pname = "registry-ui";
  version = "0.5.0";
in
  pkgs.buildGoApplication {
    inherit go pname version;

    src = ./.;
    CGO_ENABLED = 0;
    ldflags = ["-s" "-w" "-X main.version=${version}"];

    meta.mainProgram = pname;
  }
