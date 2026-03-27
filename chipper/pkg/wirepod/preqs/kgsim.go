package processreqs

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/chipper/pkg/logger"
	"github.com/kercre123/chipper/pkg/vars"
)

var BotsToInterrupt struct {
	ESNs []string
}

func containsChinese(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

func generateChineseTTS(text string) string {
	if !containsChinese(text) {
		return ""
	}

	logger.Println("Generating Chinese TTS for: " + text)

	tmpFile := os.TempDir() + "\\chinese_tts_" + fmt.Sprintf("%d", time.Now().UnixNano()) + ".wav"

	escapedText := strings.ReplaceAll(text, "\"", "`\"")
	escapedText = strings.ReplaceAll(escapedText, "$", "`$")
	escapedText = strings.ReplaceAll(escapedText, "'", "''")
	escapedText = strings.ReplaceAll(escapedText, "\n", " ")
	escapedText = strings.ReplaceAll(escapedText, "\r", " ")

	script := fmt.Sprintf(`Add-Type -AssemblyName System.Speech; $synth = New-Object System.Speech.Synthesis.SpeechSynthesizer; foreach ($v in $synth.GetInstalledVoices()) { if ($v.VoiceInfo.Description -match "Chinese" -or $v.VoiceInfo.Culture.Name -match "zh") { $synth.SelectVoice($v.VoiceInfo.Name); break } }; $stream = [System.IO.File]::Create('%s'); $format = New-Object System.Speech.AudioFormat.SpeechAudioFormatInfo(16000, [System.Speech.AudioFormat.AudioBitsPerSample]::Sixteen, [System.Speech.AudioFormat.AudioChannel]::Mono); $synth.SetOutputToAudioStream($stream, $format); $synth.Rate = -1; $synth.Speak("%s"); $stream.Close()`, tmpFile, escapedText)

	cmd := exec.Command("powershell", "-ExecutionPolicy", "Bypass", "-Command", script)
	err := cmd.Run()
	if err != nil {
		logger.Println("TTS generation error: " + err.Error())
		return ""
	}

	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		logger.Println("TTS file was not created")
		return ""
	}

	logger.Println("Chinese TTS saved to: " + tmpFile)
	return tmpFile
}

func streamAudioToVector(robot *vector.Vector, ctx context.Context, audioFile string, esn string) error {
	logger.Println("Streaming Chinese audio to Vector " + esn + "...")

	file, err := os.Open(audioFile)
	if err != nil {
		logger.Println("Error opening audio file: " + err.Error())
		return err
	}
	defer file.Close()

	header := make([]byte, 44)
	_, err = file.Read(header)
	if err != nil {
		logger.Println("Error reading WAV header: " + err.Error())
		return err
	}

	stream, err := robot.Conn.ExternalAudioStreamPlayback(ctx)
	if err != nil {
		logger.Println("Error creating audio stream: " + err.Error())
		return err
	}

	err = stream.Send(&vectorpb.ExternalAudioStreamRequest{
		AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamPrepare{
			AudioStreamPrepare: &vectorpb.ExternalAudioStreamPrepare{
				AudioFrameRate: 16000,
				AudioVolume:    100,
			},
		},
	})
	if err != nil {
		logger.Println("Error sending prepare: " + err.Error())
		return err
	}

	chunk := make([]byte, 4096)
	for {
		n, err := file.Read(chunk)
		if n == 0 {
			break
		}
		if err != nil {
			break
		}

		err = stream.Send(&vectorpb.ExternalAudioStreamRequest{
			AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamChunk{
				AudioStreamChunk: &vectorpb.ExternalAudioStreamChunk{
					AudioChunkSizeBytes: uint32(n),
					AudioChunkSamples:   chunk[:n],
				},
			},
		})
		if err != nil {
			logger.Println("Error sending chunk: " + err.Error())
			break
		}
		time.Sleep(time.Millisecond * 10)
	}

	err = stream.Send(&vectorpb.ExternalAudioStreamRequest{
		AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamComplete{
			AudioStreamComplete: &vectorpb.ExternalAudioStreamComplete{},
		},
	})
	if err != nil {
		logger.Println("Error sending complete: " + err.Error())
	}

	stream.CloseSend()

	_, err = stream.Recv()
	if err != nil {
		logger.Println("Audio stream response: " + err.Error())
	}

	logger.Println("Chinese audio streamed successfully")
	return nil
}

func ShouldBeInterrupted(esn string) bool {
	for _, sn := range BotsToInterrupt.ESNs {
		if esn == sn {
			RemoveFromInterrupt(esn)
			return true
		}
	}
	return false
}

func Interrupt(esn string) {
	BotsToInterrupt.ESNs = append(BotsToInterrupt.ESNs, esn)
}

func RemoveFromInterrupt(esn string) {
	var newList []string
	for _, bot := range BotsToInterrupt.ESNs {
		if bot != esn {
			newList = append(newList, bot)
		}
	}
	BotsToInterrupt.ESNs = newList
}

func KGSim(esn string, textToSay string) error {
	ctx := context.Background()
	matched := false
	var robot *vector.Vector
	var guid string
	var target string
	for _, bot := range vars.BotInfo.Robots {
		if esn == bot.Esn {
			guid = bot.GUID
			target = bot.IPAddress + ":443"
			matched = true
			break
		}
	}
	if matched {
		var err error
		robot, err = vector.New(vector.WithSerialNo(esn), vector.WithToken(guid), vector.WithTarget(target))
		if err != nil {
			return err
		}
	}
	controlRequest := &vectorpb.BehaviorControlRequest{
		RequestType: &vectorpb.BehaviorControlRequest_ControlRequest{
			ControlRequest: &vectorpb.ControlRequest{
				Priority: vectorpb.ControlRequest_OVERRIDE_BEHAVIORS,
			},
		},
	}
	go func() {
		start := make(chan bool)
		stop := make(chan bool)

		go func() {
			// * begin - modified from official vector-go-sdk
			r, err := robot.Conn.BehaviorControl(
				ctx,
			)
			if err != nil {
				log.Println(err)
				return
			}

			if err := r.Send(controlRequest); err != nil {
				log.Println(err)
				return
			}

			for {
				ctrlresp, err := r.Recv()
				if err != nil {
					log.Println(err)
					return
				}
				if ctrlresp.GetControlGrantedResponse() != nil {
					start <- true
					break
				}
			}

			for {
				select {
				case <-stop:
					logger.Println("KGSim: releasing behavior control (interrupt)")
					if err := r.Send(
						&vectorpb.BehaviorControlRequest{
							RequestType: &vectorpb.BehaviorControlRequest_ControlRelease{
								ControlRelease: &vectorpb.ControlRelease{},
							},
						},
					); err != nil {
						log.Println(err)
						return
					}
					return
				default:
					continue
				}
			}
			// * end - modified from official vector-go-sdk
		}()

		var stopTTSLoop bool
		var TTSLoopStopped bool
		for range start {
			time.Sleep(time.Millisecond * 300)
			robot.Conn.PlayAnimation(
				ctx,
				&vectorpb.PlayAnimationRequest{
					Animation: &vectorpb.Animation{
						Name: "anim_getin_tts_01",
					},
					Loops: 1,
				},
			)
			go func() {
				for {
					if stopTTSLoop {
						TTSLoopStopped = true
						break
					}
					robot.Conn.PlayAnimation(
						ctx,
						&vectorpb.PlayAnimationRequest{
							Animation: &vectorpb.Animation{
								Name: "anim_tts_loop_02",
							},
							Loops: 1,
						},
					)
				}
			}()
			var stopTTS bool
			go func() {
				for {
					time.Sleep(time.Millisecond * 50)
					if ShouldBeInterrupted(esn) {
						RemoveFromInterrupt(esn)
						robot.Conn.SayText(
							ctx,
							&vectorpb.SayTextRequest{
								Text:           "",
								UseVectorVoice: true,
								DurationScalar: 1.0,
							},
						)
						stop <- true
						stopTTSLoop = true
						stopTTS = true
						break
					}
				}
			}()

			// Check if text contains Chinese characters
			if containsChinese(textToSay) {
				logger.Println("Chinese text detected, generating Chinese TTS...")
				audioFile := generateChineseTTS(textToSay)
				if audioFile != "" {
					err := streamAudioToVector(robot, ctx, audioFile, esn)
					if err != nil {
						logger.Println("Error streaming Chinese audio: " + err.Error())
						// Fallback to English TTS
						robot.Conn.SayText(
							ctx,
							&vectorpb.SayTextRequest{
								Text:           textToSay,
								UseVectorVoice: true,
								DurationScalar: 1.0,
							},
						)
					}
					// Clean up temp file
					go func() {
						time.Sleep(time.Second * 5)
						os.Remove(audioFile)
					}()
				} else {
					// Fallback to English TTS
					robot.Conn.SayText(
						ctx,
						&vectorpb.SayTextRequest{
							Text:           textToSay,
							UseVectorVoice: true,
							DurationScalar: 1.0,
						},
					)
				}
			} else {
				// English TTS (original logic)
				textToSaySplit := strings.Split(textToSay, ". ")
				for _, str := range textToSaySplit {
					_, err := robot.Conn.SayText(
						ctx,
						&vectorpb.SayTextRequest{
							Text:           str + ".",
							UseVectorVoice: true,
							DurationScalar: 1.0,
						},
					)
					if err != nil {
						logger.Println("KG SayText error: " + err.Error())
						stop <- true
						break
					}
					if stopTTS {
						return
					}
				}
			}
			stopTTSLoop = true
			for {
				if TTSLoopStopped {
					break
				} else {
					time.Sleep(time.Millisecond * 10)
				}
			}
			time.Sleep(time.Millisecond * 100)
			robot.Conn.PlayAnimation(
				ctx,
				&vectorpb.PlayAnimationRequest{
					Animation: &vectorpb.Animation{
						Name: "anim_knowledgegraph_success_01",
					},
					Loops: 1,
				},
			)
			//time.Sleep(time.Millisecond * 3300)
			stop <- true
		}
	}()
	return nil
}
