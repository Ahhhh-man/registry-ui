{
  pkgs,
  app,
  rev ? "unknown",
}:
pkgs.dockerTools.buildLayeredImage {
  name = "registry-ui";
  tag = "latest";

  # CA bundle so the UI can reach registries over HTTPS.
  contents = [pkgs.cacert];

  # The binary sits at /registry-ui so compose healthchecks can call it by a fixed path.
  extraCommands = ''
    cp ${pkgs.lib.getExe app} registry-ui
  '';

  config = {
    Entrypoint = ["/registry-ui"];
    User = "65532:65532";
    ExposedPorts."8080/tcp" = {};
    Env = ["SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"];
    Labels = {
      "org.opencontainers.image.source" = "https://github.com/Ahhhh-man/registry-ui";
      "org.opencontainers.image.version" = app.version;
      "org.opencontainers.image.revision" = rev;
      "org.opencontainers.image.licenses" = "MIT";
      "org.opencontainers.image.description" = "A lightweight web UI for self-hosted Docker/OCI registries";
    };
  };
}
