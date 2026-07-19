# vboard — a GTK3 on-screen keyboard, packaged from source because it is not in
# nixpkgs. It injects keystrokes via /dev/uinput (kernel level, so it works
# under any compositor including Sway) and, crucially for a kiosk, marks its
# window non-focusable (set_accept_focus/can_focus false) so the injected keys
# land in whatever is actually focused (Chromium), not the keyboard itself.
#
# It is a Meson project that installs a thin `vboard` launcher into bin/ and the
# real Python package under share/vboard/vboard; the launcher adds
# ../share/vboard to sys.path. We point its shebang at a Python that has
# PyGObject + python-uinput, and wrapGAppsHook3 supplies the GI typelibs (Gtk,
# Gdk, AyatanaAppIndicator3) and icon/schema paths at runtime.
{
  lib,
  stdenv,
  fetchFromGitHub,
  meson,
  ninja,
  pkg-config,
  wrapGAppsHook3,
  gobject-introspection,
  glib,
  gtk3,
  gdk-pixbuf,
  librsvg,
  libayatana-appindicator,
  python3,
}:

let
  pythonEnv = python3.withPackages (ps: [
    ps.pygobject3
    ps.pycairo
    ps.python-uinput
  ]);
in
stdenv.mkDerivation (finalAttrs: {
  pname = "vboard";
  version = "2.5.0";

  src = fetchFromGitHub {
    owner = "archisman-panigrahi";
    repo = "vboard";
    rev = "v${finalAttrs.version}";
    hash = "sha256-oM2i+dB3i8Frc41Db1WCN5DhD6lvQ3aP+VwQXeCsHxc=";
  };

  # Nix installs meson projects straight into $out (no DESTDIR), so the upstream
  # post-install hook's DESTDIR guard never fires and it would run
  # gtk-update-icon-cache / update-desktop-database / KDE cache refresh against
  # the store. None of that is needed here, so neuter it to a no-op.
  postPatch = ''
    printf '#!/bin/sh\nexit 0\n' > scripts/meson-post-install.sh
  '';

  nativeBuildInputs = [
    meson
    ninja
    pkg-config
    wrapGAppsHook3
    gobject-introspection
  ];

  buildInputs = [
    glib
    gtk3
    gdk-pixbuf
    librsvg
    libayatana-appindicator
    pythonEnv
  ];

  # The launcher is installed with a `/usr/bin/env python3` shebang; repoint it
  # at the interpreter that actually has PyGObject + python-uinput. wrapGAppsHook3
  # then wraps it for the GI typelibs and icon/schema paths.
  postInstall = ''
    substituteInPlace $out/bin/vboard \
      --replace-fail '#!/usr/bin/env python3' '#!${pythonEnv.interpreter}'
  '';

  meta = {
    description = "Customizable on-screen keyboard for Linux (uinput injection)";
    homepage = "https://github.com/archisman-panigrahi/vboard";
    license = lib.licenses.gpl3Plus;
    mainProgram = "vboard";
    platforms = lib.platforms.linux;
  };
})
