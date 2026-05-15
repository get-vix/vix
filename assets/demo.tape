# VHS documentation
#
# Output:
#   Output <path>.gif               Create a GIF output at the given <path>
#   Output <path>.mp4               Create an MP4 output at the given <path>
#   Output <path>.webm              Create a WebM output at the given <path>
#
# Require:
#   Require <string>                Ensure a program is on the $PATH to proceed
#
# Settings:
#   Set FontSize <number>           Set the font size of the terminal
#   Set FontFamily <string>         Set the font family of the terminal
#   Set Height <number>             Set the height of the terminal
#   Set Width <number>              Set the width of the terminal
#   Set LetterSpacing <float>       Set the font letter spacing (tracking)
#   Set LineHeight <float>          Set the font line height
#   Set LoopOffset <float>%         Set the starting frame offset for the GIF loop
#   Set Theme <json|string>         Set the theme of the terminal
#   Set Padding <number>            Set the padding of the terminal
#   Set Framerate <number>          Set the framerate of the recording
#   Set PlaybackSpeed <float>       Set the playback speed of the recording
#   Set MarginFill <file|#000000>   Set the file or color the margin will be filled with.
#   Set Margin <number>             Set the size of the margin. Has no effect if MarginFill isn't set.
#   Set BorderRadius <number>       Set terminal border radius, in pixels.
#   Set WindowBar <string>          Set window bar type. (one of: Rings, RingsRight, Colorful, ColorfulRight)
#   Set WindowBarSize <number>      Set window bar size, in pixels. Default is 40.
#   Set TypingSpeed <time>          Set the typing speed of the terminal. Default is 50ms.
#
# Sleep:
#   Sleep <time>                    Sleep for a set amount of <time> in seconds
#
# Type:
#   Type[@<time>] "<characters>"    Type <characters> into the terminal with a
#                                   <time> delay between each character
#
# Keys:
#   Escape[@<time>] [number]        Press the Escape key
#   Backspace[@<time>] [number]     Press the Backspace key
#   Delete[@<time>] [number]        Press the Delete key
#   Insert[@<time>] [number]        Press the Insert key
#   Down[@<time>] [number]          Press the Down key
#   Enter[@<time>] [number]         Press the Enter key
#   Space[@<time>] [number]         Press the Space key
#   Tab[@<time>] [number]           Press the Tab key
#   Left[@<time>] [number]          Press the Left Arrow key
#   Right[@<time>] [number]         Press the Right Arrow key
#   Up[@<time>] [number]            Press the Up Arrow key
#   Down[@<time>] [number]          Press the Down Arrow key
#   PageUp[@<time>] [number]        Press the Page Up key
#   PageDown[@<time>] [number]      Press the Page Down key
#   Ctrl+<key>                      Press the Control key + <key> (e.g. Ctrl+C)
#
# Display:
#   Hide                            Hide the subsequent commands from the output
#   Show                            Show the subsequent commands in the output
#   ScrollUp[@<time>] [number]      Scroll terminal viewport up by rows
#   ScrollDown[@<time>] [number]    Scroll terminal viewport down by rows

Output demo.gif

Require echo

Set Shell "bash"
Set FontSize 32
Set Width 2400
Set Height 1500
Set TypingSpeed 75ms

# Launch vix
# (vixd should already be running)
Sleep 500ms
Type "vix --appear-as-cc" 
Sleep 1000ms  
Enter
Sleep 4000ms 
Enter

# First message
Type "Hi!"
Sleep 500ms
Enter
Sleep 3000ms

# Hinting at command palette
Type "In vix you can access the command palette with ctrl+P and create new tab"
Sleep 500ms

# Show command palette
Ctrl+p
Sleep 5000ms 
Type "Tab"
Sleep 500ms
Enter
Sleep 500ms

# Hinting at the history
# Ctrl+U
# Sleep 100ms
# Type "And go through prompt history with ctrl+R"
# Sleep 500ms

# Show history
# Ctrl+r
# Sleep 1000ms
# Up
# Sleep 500ms
# Up
# Sleep 500ms
# Up
# Sleep 500ms
# Up
# Sleep 3000ms
# Escape
# Sleep 1s

# Show off markdown rendering
Ctrl+U
Type "Now write a file tmp.go" just to show off your capabilities
Enter
Sleep 10000ms
# Validate authorization to write
Enter
Sleep 7000ms

# Show plan mode
Shift+Tab
Sleep 500ms
Type "Come up with a random plan for improving the file tmp.go" 
Sleep 1000ms
Enter
Sleep 20000ms