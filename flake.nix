{
  description = "vix dev shell";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
    in
    {
      devShells = nixpkgs.lib.genAttrs systems (system:
        let pkgs = nixpkgs.legacyPackages.${system};
        in {
          default = pkgs.mkShellNoCC {
            # Add pkgs.nodejs and pkgs.npm when internal/daemon/web/source/ is present
            # (the web UI source is a private submodule).
            packages = [
              pkgs.go
              pkgs.gcc
              pkgs.gnumake
              pkgs.patch
              pkgs.gopls
              pkgs.go-tools
              pkgs.golangci-lint
            ];

            shellHook = ''
              export CGO_ENABLED=1
              echo "go $(go version)"
              echo "gcc $(gcc --version | head -1)"
            '';
          };
        });
    };
}
