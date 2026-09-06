"""Convert audio to the Ogg Opus format WhatsApp uses for voice notes."""

from __future__ import annotations

import os
import shutil
import subprocess
import tempfile


def ffmpeg_available() -> bool:
    return shutil.which("ffmpeg") is not None


def is_ogg_opus(path: str) -> bool:
    """Cheap check: Ogg capture pattern and an OpusHead packet near the start."""
    try:
        with open(path, "rb") as fh:
            head = fh.read(512)
    except OSError:
        return False
    return head.startswith(b"OggS") and b"OpusHead" in head


def convert_to_opus_ogg(input_file: str, output_file: str | None = None, bitrate: str = "32k", sample_rate: int = 24000) -> str:
    """Transcode with ffmpeg. Raises FileNotFoundError / RuntimeError."""
    if not os.path.isfile(input_file):
        raise FileNotFoundError(f"Input file not found: {input_file}")
    if not ffmpeg_available():
        raise RuntimeError("ffmpeg is not installed; install it or send an .ogg Opus file directly")
    if output_file is None:
        output_file = os.path.splitext(input_file)[0] + ".ogg"
    cmd = [
        "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
        "-i", input_file,
        "-vn", "-c:a", "libopus", "-b:a", bitrate, "-ar", str(sample_rate), "-ac", "1",
        "-application", "voip", "-vbr", "on", "-compression_level", "10", "-frame_duration", "60",
        output_file,
    ]
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        raise RuntimeError(f"ffmpeg failed: {proc.stderr.strip()[:500]}")
    return output_file


def convert_to_opus_ogg_temp(input_file: str) -> str:
    """Convert into a temporary .ogg file and return its path."""
    fd, tmp = tempfile.mkstemp(suffix=".ogg", prefix="graab-voice-")
    os.close(fd)
    try:
        return convert_to_opus_ogg(input_file, tmp)
    except Exception:
        if os.path.exists(tmp):
            os.unlink(tmp)
        raise
