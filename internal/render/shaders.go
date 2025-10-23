package render

import (
	"log"
	"strings"

	"github.com/go-gl/gl/v4.1-core/gl"
)

// ShaderManager handles OpenGL shader program compilation, linking, and uniform
// management.
type ShaderManager struct {
	program    uint32 // program ID
	uTransform int32  // uniform location for transformation matrix
}

// Vertex shader. Simply applies the uniform transformation matrix to the
// vertices and forwards the color the the fragment shader.
const vertexShaderSource = `
#version 330 core
layout (location = 0) in vec2 aPos;
layout (location = 1) in vec4 aColor;

uniform mat4 uTransform;

out vec4 vColor;

void main() {
    gl_Position = uTransform * vec4(aPos, 0.0, 1.0);
    vColor = aColor;
}
` + "\x00"

// Fragment shader. Simply applies the vertex-shader forwarded color.
const fragmentShaderSource = `
#version 330 core
in vec4 vColor;
out vec4 FragColor;

void main() {
    FragColor = vColor;
}
` + "\x00"

// NewShaderManager creates and initializes a new shader manager with compiled
// and linked shaders.
func NewShaderManager() *ShaderManager {
	sm := &ShaderManager{}

	// Create and compile shaders.
	vertexShader := sm.compileShader(vertexShaderSource, gl.VERTEX_SHADER)
	defer gl.DeleteShader(vertexShader)

	fragmentShader := sm.compileShader(fragmentShaderSource, gl.FRAGMENT_SHADER)
	defer gl.DeleteShader(fragmentShader)

	// Link shader program.
	sm.program = gl.CreateProgram()
	gl.AttachShader(sm.program, vertexShader)
	gl.AttachShader(sm.program, fragmentShader)
	gl.LinkProgram(sm.program)

	// Check linking status.
	var status int32
	gl.GetProgramiv(sm.program, gl.LINK_STATUS, &status)
	if status == gl.FALSE {
		var logLength int32
		gl.GetProgramiv(sm.program, gl.INFO_LOG_LENGTH, &logLength)
		logText := strings.Repeat("\x00", int(logLength+1))
		gl.GetProgramInfoLog(sm.program, logLength, nil, gl.Str(logText))
		log.Fatalf("Shader linking failed: %s", logText)
	}

	// Get uniform location.
	sm.uTransform = gl.GetUniformLocation(sm.program, gl.Str("uTransform\x00"))
	gl.UseProgram(sm.program) // bind the shader program
	return sm
}

// SetTransform sets the uniform transformation matrix.
func (sm *ShaderManager) SetTransform(matrix [16]float32) {
	gl.UniformMatrix4fv(sm.uTransform, 1, false, &matrix[0])
}

// compileShader compiles a single shader from source.
func (sm *ShaderManager) compileShader(source string, shaderType uint32) uint32 {
	shader := gl.CreateShader(shaderType)
	csource, free := gl.Strs(source)
	gl.ShaderSource(shader, 1, csource, nil)
	free()
	gl.CompileShader(shader)

	// Check compilation status.
	var status int32
	gl.GetShaderiv(shader, gl.COMPILE_STATUS, &status)
	if status == gl.FALSE {
		var logLength int32
		gl.GetShaderiv(shader, gl.INFO_LOG_LENGTH, &logLength)
		logText := strings.Repeat("\x00", int(logLength+1))
		gl.GetShaderInfoLog(shader, logLength, nil, gl.Str(logText))
		log.Fatalf("Shader compilation failed: %s", logText)
	}

	return shader
}
