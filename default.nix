{
  pkgs,
  go,
}: let
  pname = "registry-ui";
  version = "1.0.0";
in
  pkgs.buildGoApplication {
    inherit go pname version;

    src = ./.;
    CGO_ENABLED = 0;
    ldflags = ["-s" "-w" "-X main.version=${version}"];

    meta.mainProgram = pname;
  }
