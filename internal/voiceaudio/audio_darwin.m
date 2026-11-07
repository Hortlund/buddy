//go:build darwin && cgo

#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <stdatomic.h>
#import "audio.h"
#import "_cgo_export.h"

// The replacement AVAudioEngine APIs require macOS 27. Keep the macOS 12 deployment target.
#pragma clang diagnostic ignored "-Wdeprecated-declarations"

@interface BuddyVoiceSegment : NSObject {
@public
    _Atomic int played;
    int queued;
}
@end

@implementation BuddyVoiceSegment
@end

static AVAudioFormat *buddy_wire_format(void) {
    return [[AVAudioFormat alloc] initWithCommonFormat:AVAudioPCMFormatInt16 sampleRate:24000 channels:1 interleaved:NO];
}

static void buddy_convert_mic(AVAudioPCMBuffer *buffer, AVAudioConverter *converter, uint64_t token) {
    const double ratio = converter.outputFormat.sampleRate / buffer.format.sampleRate;
    AVAudioFrameCount capacity = (AVAudioFrameCount)ceil(buffer.frameLength * ratio) + 64;
    AVAudioPCMBuffer *out = [[AVAudioPCMBuffer alloc] initWithPCMFormat:converter.outputFormat frameCapacity:capacity];
    if (!out) return;
    __block BOOL supplied = NO;
    AVAudioConverterOutputStatus status = [converter convertToBuffer:out error:nil withInputFromBlock:^AVAudioBuffer *(AVAudioPacketCount count, AVAudioConverterInputStatus *inputStatus) {
        if (supplied) {
            *inputStatus = AVAudioConverterInputStatus_NoDataNow;
            return nil;
        }
        supplied = YES;
        *inputStatus = AVAudioConverterInputStatus_HaveData;
        return buffer;
    }];
    if (status == AVAudioConverterOutputStatus_Error) return;
    for (AVAudioFrameCount offset = 0; offset < out.frameLength; offset += 960) {
        int frames = (int)MIN(960, out.frameLength - offset);
        buddyVoicePCM(token, out.int16ChannelData[0] + offset, frames * 2);
    }
}

// Direct capture uses the system input without creating a duplex
// voice-processing device with the current speakers.
@interface BuddyDirectMic : NSObject <AVCaptureAudioDataOutputSampleBufferDelegate>
@property(nonatomic, strong) AVCaptureSession *session;
@property(nonatomic, strong) AVCaptureAudioDataOutput *output;
@property(nonatomic, strong) AVAudioConverter *converter;
@property(nonatomic) uint64_t token;
- (BOOL)start;
- (void)stop;
@end

@implementation BuddyDirectMic
- (BOOL)start {
    AVCaptureDevice *device = [AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeAudio];
    if (!device) return NO;
    AVCaptureDeviceInput *input = [AVCaptureDeviceInput deviceInputWithDevice:device error:nil];
    if (!input) return NO;
    self.session = [AVCaptureSession new];
    self.output = [AVCaptureAudioDataOutput new];
    self.output.audioSettings = @{
        AVFormatIDKey: @(kAudioFormatLinearPCM),
        AVSampleRateKey: @24000,
        AVNumberOfChannelsKey: @1,
        AVLinearPCMBitDepthKey: @16,
        AVLinearPCMIsFloatKey: @NO,
        AVLinearPCMIsBigEndianKey: @NO,
    };
    if (![self.session canAddInput:input] || ![self.session canAddOutput:self.output]) return NO;
    [self.session addInput:input];
    [self.session addOutput:self.output];
    dispatch_queue_t queue = dispatch_queue_create("dev.buddy.microphone", DISPATCH_QUEUE_SERIAL);
    [self.output setSampleBufferDelegate:self queue:queue];
    [self.session startRunning];
    return self.session.isRunning;
}

- (void)captureOutput:(AVCaptureOutput *)output
didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
      fromConnection:(AVCaptureConnection *)connection {
    CMFormatDescriptionRef format = CMSampleBufferGetFormatDescription(sampleBuffer);
    const AudioStreamBasicDescription *asbd = format ? CMAudioFormatDescriptionGetStreamBasicDescription(format) : NULL;
    if (!asbd || asbd->mFormatID != kAudioFormatLinearPCM || asbd->mSampleRate <= 0) return;

    CMItemCount samples = CMSampleBufferGetNumSamples(sampleBuffer);
    if (samples <= 0 || samples > 192000) return;
    if (asbd->mSampleRate != 24000 || asbd->mChannelsPerFrame != 1 || asbd->mBytesPerFrame != 2 ||
        asbd->mBitsPerChannel != 16 || (asbd->mFormatFlags & (kAudioFormatFlagIsFloat | kAudioFormatFlagIsBigEndian))) {
        // Some devices initially deliver their native format after permission
        // is granted. Convert these frames instead of dropping the input.
        AVAudioFormat *source = [[AVAudioFormat alloc] initWithStreamDescription:asbd];
        if (!source) return;
        if (![self.converter.inputFormat isEqual:source]) {
            self.converter = [[AVAudioConverter alloc] initFromFormat:source toFormat:buddy_wire_format()];
        }
        if (!self.converter) return;
        AVAudioPCMBuffer *buffer = [[AVAudioPCMBuffer alloc] initWithPCMFormat:source frameCapacity:(AVAudioFrameCount)samples];
        if (!buffer) return;
        buffer.frameLength = (AVAudioFrameCount)samples;
        if (CMSampleBufferCopyPCMDataIntoAudioBufferList(sampleBuffer, 0, (int32_t)samples, buffer.mutableAudioBufferList) == noErr) {
            buddy_convert_mic(buffer, self.converter, self.token);
        }
        return;
    }
    int16_t pcm[960];
    for (CMItemCount offset = 0; offset < samples; offset += 960) {
        int32_t count = (int32_t)MIN(960, samples - offset);
        AudioBufferList buffer = {1, {{1, (uint32_t)(count * sizeof(int16_t)), pcm}}};
        if (CMSampleBufferCopyPCMDataIntoAudioBufferList(sampleBuffer, (int32_t)offset, count, &buffer) == noErr) {
            buddyVoicePCM(self.token, pcm, count * (int)sizeof(int16_t));
        }
    }
}

- (void)stop {
    [self.output setSampleBufferDelegate:nil queue:NULL];
    [self.session stopRunning];
}
@end

@interface BuddyVoiceAudio : NSObject
@property(nonatomic, strong) AVAudioEngine *engine;
@property(nonatomic, strong) AVAudioPlayerNode *player;
@property(nonatomic, strong) AVAudioFormat *playFormat;
@property(nonatomic, strong) BuddyVoiceSegment *segment;
@property(nonatomic) BOOL capturing;
@property(nonatomic, strong) BuddyDirectMic *microphone;
@end

@implementation BuddyVoiceAudio
@end

void buddy_voice_permission(uint64_t token) {
    @autoreleasepool {
        switch ([AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio]) {
            case AVAuthorizationStatusAuthorized: buddyVoicePermission(token, 1, 0); break;
            case AVAuthorizationStatusNotDetermined:
                [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL allowed) {
                    buddyVoicePermission(token, allowed ? 1 : 0, 1);
                }];
                break;
            default: buddyVoicePermission(token, 0, 0); break;
        }
    }
}

void *buddy_voice_open(uint64_t token, bool direct, bool playback) {
    @autoreleasepool {
        BuddyVoiceAudio *a = [BuddyVoiceAudio new];
        @try {
            if (direct) {
                a.microphone = [BuddyDirectMic new];
                a.microphone.token = token;
                if (![a.microphone start]) {
                    [a.microphone stop];
                    return NULL;
                }
            }
            if (!playback) return (__bridge_retained void *)a;

            a.engine = [AVAudioEngine new];
            a.playFormat = [[AVAudioFormat alloc] initStandardFormatWithSampleRate:24000 channels:1];
            a.player = [AVAudioPlayerNode new];
            a.segment = [BuddyVoiceSegment new];
            NSError *error = nil;
            if (!direct) {
                AVAudioInputNode *input = a.engine.inputNode;
                // Keep input and output in the same engine so voice processing can
                // cancel speaker echo while the person is talking.
                if (![input setVoiceProcessingEnabled:YES error:&error]) {
                    return NULL;
                }
                AVAudioFormat *source = [input outputFormatForBus:0];
                if (source.sampleRate <= 0 || source.channelCount == 0) return NULL;
                AVAudioFormat *wire = buddy_wire_format();
                AVAudioConverter *converter = [[AVAudioConverter alloc] initFromFormat:source toFormat:wire];
                if (!converter) return NULL;

                [input installTapOnBus:0 bufferSize:1024 format:source block:^(AVAudioPCMBuffer *buffer, AVAudioTime *time) {
                    buddy_convert_mic(buffer, converter, token);
                }];
                a.capturing = YES;
            }
            [a.engine attachNode:a.player];
            [a.engine connect:a.player to:a.engine.mainMixerNode format:a.playFormat];
            [a.engine prepare];
            if (![a.engine startAndReturnError:&error]) {
                [a.microphone stop];
                if (a.capturing) [a.engine.inputNode removeTapOnBus:0];
                return NULL;
            }
        } @catch (NSException *e) {
            [a.microphone stop];
            [a.engine stop];
            if (a.capturing) [a.engine.inputNode removeTapOnBus:0];
            return NULL;
        }
        return (__bridge_retained void *)a;
    }
}

bool buddy_voice_running(void *audio) {
    @autoreleasepool {
        BuddyVoiceAudio *a = (__bridge BuddyVoiceAudio *)audio;
        return (!a.engine || a.engine.isRunning) && (!a.microphone || a.microphone.session.isRunning);
    }
}

void *buddy_voice_ring(const void *wave, int count) {
    @autoreleasepool {
        NSData *data = [NSData dataWithBytes:wave length:count];
        AVAudioPlayer *player = [[AVAudioPlayer alloc] initWithData:data error:nil];
        player.numberOfLoops = -1;
        if (![player play]) return NULL;
        return (__bridge_retained void *)player;
    }
}

void buddy_voice_stop_ring(void *ring) {
    @autoreleasepool {
        AVAudioPlayer *player = (__bridge_transfer AVAudioPlayer *)ring;
        [player stop];
    }
}

bool buddy_voice_play(void *audio, const void *pcm, int frames) {
    @autoreleasepool {
        BuddyVoiceAudio *a = (__bridge BuddyVoiceAudio *)audio;
        BuddyVoiceSegment *segment = a.segment;
        if (!a.engine.isRunning || segment->queued - atomic_load(&segment->played) + frames > 24000 * 45) return false;
        AVAudioPCMBuffer *buffer = [[AVAudioPCMBuffer alloc] initWithPCMFormat:a.playFormat frameCapacity:frames];
        if (!buffer) return false;
        buffer.frameLength = frames;
        float *dst = buffer.floatChannelData[0];
        const unsigned char *src = pcm;
        for (int i = 0; i < frames; i++) {
            dst[i] = (int16_t)((uint16_t)src[i*2] | ((uint16_t)src[i*2+1] << 8)) / 32768.0f;
        }
        @try {
            [a.player scheduleBuffer:buffer completionCallbackType:AVAudioPlayerNodeCompletionDataPlayedBack completionHandler:^(AVAudioPlayerNodeCompletionCallbackType type) {
                atomic_fetch_add(&segment->played, frames);
            }];
            segment->queued += frames;
            if (!a.player.isPlaying) [a.player play];
        } @catch (NSException *e) {
            return false;
        }
        return true;
    }
}

bool buddy_voice_drained(void *audio) {
    @autoreleasepool {
        BuddyVoiceSegment *s = ((__bridge BuddyVoiceAudio *)audio).segment;
        return atomic_load(&s->played) >= s->queued;
    }
}

int buddy_voice_reset(void *audio) {
    @autoreleasepool {
        BuddyVoiceAudio *a = (__bridge BuddyVoiceAudio *)audio;
        BuddyVoiceSegment *s = a.segment;
        AVAudioTime *render = a.player.lastRenderTime;
        AVAudioTime *now = render ? [a.player playerTimeForNodeTime:render] : nil;
        int played = atomic_load(&s->played);
        if (now && now.sampleTimeValid) {
            AVAudioFramePosition frames = now.sampleTime - a.player.outputPresentationLatency * now.sampleRate;
            if (frames > played) played = (int)MIN(frames, s->queued);
        }
        [a.player stop];
        a.segment = [BuddyVoiceSegment new];
        return MIN(played, s->queued) / 24;
    }
}

void buddy_voice_close(void *audio) {
    @autoreleasepool {
        BuddyVoiceAudio *a = (__bridge_transfer BuddyVoiceAudio *)audio;
        [a.microphone stop];
        [a.player stop];
        [a.engine stop];
        if (a.capturing) [a.engine.inputNode removeTapOnBus:0];
    }
}
