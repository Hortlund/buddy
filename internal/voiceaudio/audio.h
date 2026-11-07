#include <stdbool.h>
#include <stdint.h>

void buddy_voice_permission(uint64_t token);
void *buddy_voice_open(uint64_t token, bool direct, bool playback);
bool buddy_voice_running(void *audio);
void *buddy_voice_ring(const void *wave, int count);
void buddy_voice_stop_ring(void *ring);
bool buddy_voice_play(void *audio, const void *pcm, int frames);
bool buddy_voice_drained(void *audio);
int buddy_voice_reset(void *audio);
void buddy_voice_close(void *audio);
