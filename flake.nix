{
  description = "Nix infrastructure hub, deployer and beacon";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/e554fab72f81915600f3f449b786fd9af40439a5";
  outputs =
    { nixpkgs, ... }:
    let
      each = nixpkgs.lib.genAttrs [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
    in
    {
      devShells = each (system: {
        default = nixpkgs.legacyPackages.${system}.mkShell {
          packages = with nixpkgs.legacyPackages.${system}; [
            go
            gnumake
            git
            nodejs
          ];
        };
      });
      formatter = each (system: nixpkgs.legacyPackages.${system}.nixfmt);
    };
}
