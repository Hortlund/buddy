"""Build the editable Bud and export its meshes and motion clips.

Run: Blender --background --factory-startup --python assets/character/rig.py
"""
import math
import struct
from pathlib import Path

import bpy
from mathutils import Vector

ROOT = Path(__file__).resolve().parents[2]
SHAPES = ("orb", "bean", "pebble", "drop", "cloud", "capsule", "pillow")
meshes = []

# Positions in the avatar use +Y up and +Z toward the viewer.
def xyz(v):
    return (v[0], -v[2], v[1])


def ellipsoid(name, material, deform, segments=64, rings=40, export=True):
    vertices = [(0, 1, 0)]
    for j in range(1, rings):
        lat = math.pi * j / rings
        for i in range(segments):
            lon = 2 * math.pi * i / segments
            vertices.append((math.sin(lat) * math.cos(lon), math.cos(lat), math.sin(lat) * math.sin(lon)))
    vertices.append((0, -1, 0))
    faces = [(0, i+1, (i+1) % segments+1) for i in range(segments)]
    for j in range(rings-2):
        for i in range(segments):
            a, b = 1+j*segments+i, 1+j*segments+(i+1) % segments
            faces.append((a, a+segments, b+segments, b))
    bottom = len(vertices)-1
    faces += [(bottom, bottom-segments+(i+1) % segments, bottom-segments+i) for i in range(segments)]
    return mesh_object(name, material, [deform(*v) for v in vertices], faces, export)


def mesh_object(name, material, vertices, faces, export=True):
    mesh = bpy.data.meshes.new(name)
    mesh.from_pydata([xyz(v) for v in vertices], [], faces)
    mesh.update()
    obj = bpy.data.objects.new(name, mesh)
    bpy.context.collection.objects.link(obj)
    mesh.materials.append(material)
    for face in mesh.polygons:
        face.use_smooth = True
    # The mantle and caps are generated independently. Recalculate consistent outward normals.
    import bmesh
    model = bmesh.new()
    model.from_mesh(mesh)
    bmesh.ops.recalc_face_normals(model, faces=model.faces)
    model.to_mesh(mesh)
    model.free()
    mesh.update()
    if export:
        meshes.append(obj)
    return obj


def material(name, hex_color, roughness=.52):
    color = tuple(int(hex_color[i:i+2], 16)/255 for i in (0, 2, 4))
    linear = tuple(((v + .055) / 1.055)**2.4 if v > .04045 else v / 12.92 for v in color) + (1,)
    surface = bpy.data.materials.new(name)
    surface.diffuse_color = linear
    surface.use_nodes = True
    node = surface.node_tree.nodes.get("Principled BSDF")
    node.inputs["Base Color"].default_value = linear
    node.inputs["Roughness"].default_value = roughness
    return surface


def body_shape(kind, x, y, z):
    angle = math.atan2(y, x)
    ring = (x*x+y*y)**.5
    if kind == "orb":
        r = 1 + ring*ring*(.012*math.cos(3*angle+.5) + .009*math.sin(5*angle))
        return (x*1.00*r-.057*y, .87*y*r+.035*x, .74*z)
    if kind == "bean":
        return (x*(.95-.17*y)-.23*y*y+.17*y, .81*y+.19*x, .69*z)
    if kind == "drop":
        return (x*(1-.31*y)+.08*y, .87*y + .075*y*y, .72*z*(1-.21*y))
    if kind == "cloud":
        lobe = 1+.068*ring*ring*math.cos(5*angle+.5)+.012*x
        return (x*1.03*lobe, y*.79*lobe, z*.71)
    # Squircle sections stay round in depth, with softer corners on the pillow.
    exponent = {"pillow": .72, "pebble": .86, "capsule": .94}[kind]
    def rounded(v):
        return math.copysign(abs(v)**exponent, v)
    height = {"pillow": .78, "pebble": .72, "capsule": .64}[kind]
    return (rounded(x)*1.00-.07*y, rounded(y)*height+.07*x, rounded(z)*.68)


def rounded_box(name, size, bevel, surface, pos=(0, 0, 0)):
    bpy.ops.mesh.primitive_cube_add(size=1, location=xyz(pos))
    obj = bpy.context.object
    obj.name = name
    obj.scale = xyz((size[0], size[1], -size[2]))
    bpy.ops.object.transform_apply(location=False, rotation=False, scale=True)
    modifier = obj.modifiers.new("Rounded edges", "BEVEL")
    modifier.width, modifier.segments = bevel, 5
    bpy.ops.object.modifier_apply(modifier=modifier.name)
    obj.data.materials.append(surface)
    for polygon in obj.data.polygons:
        polygon.use_smooth = True
    modifier = obj.modifiers.new("Weighted corner normals", "WEIGHTED_NORMAL")
    modifier.keep_sharp = True
    bpy.ops.object.modifier_apply(modifier=modifier.name)
    return obj


def merge(name, objects):
    bpy.ops.object.select_all(action="DESELECT")
    for obj in objects:
        obj.select_set(True)
    bpy.context.view_layer.objects.active = objects[0]
    bpy.ops.object.join()
    bpy.ops.object.transform_apply(location=True, rotation=True, scale=True)
    obj = bpy.context.object
    obj.name = name
    meshes.append(obj)
    return obj


def empty(name, parent=None):
    obj = bpy.data.objects.new(name, None)
    bpy.context.collection.objects.link(obj)
    obj.parent = parent
    obj.empty_display_size = .12
    return obj


def set_transform(obj, pos=(0, 0, 0), roll=0, scale=(1, 1, 1)):
    obj.location = xyz(pos)
    obj.rotation_euler = (0, -math.radians(roll), 0)
    obj.scale = (scale[0], scale[2], scale[1])


bpy.ops.wm.read_factory_settings(use_empty=True)
body = material("01 · Glazed terracotta", "ED815C")
eye = material("02 · Espresso eyes", "301D26", .35)
page = material("03 · Paper", "FFECC4")
cover = material("04 · Indigo cover", "514D83")
metal = material("05 · Brushed brass", "E9AC53", .37)
grip = material("06 · Plum grip", "74435E")
glow = material("07 · Lemon enamel", "FFD567", .38)
spine = material("08 · Blue stitching", "A99AC3")

bodies = {}
anchors = {}
for shape in SHAPES:
    obj = ellipsoid("Body · " + shape.title(), body, lambda x, y, z: body_shape(shape, x, y, z))
    bodies[shape] = obj
    anchors[shape] = []
    for x, y in ((-.225, .125), (.298, .216)):
        hits, point, normal, _ = obj.ray_cast(Vector(xyz((x, y, 2))), Vector(xyz((0, 0, -1))))
        assert hits
        anchors[shape].append((x, y, -point.y + .029))
    if shape != "orb":
        obj.hide_render = True
        obj.hide_set(True)

tuft = ellipsoid("Little folded crest", body, lambda x,y,z: (x*.137-.13*y, y*.195, z*.13), 24, 16)
# Give the lobes straight capsule sides, rounded ends and a low, smooth 3D dome.
eyes = ellipsoid("Long pill eye", eye,
                 lambda x,y,z: (x*.125, .162*y+.108*math.tanh(y*7), z*.046),
                 48, 36)

bpy.ops.mesh.primitive_torus_add(location=xyz((0, .18, 0)), major_segments=48, minor_segments=16,
                                 major_radius=.247, minor_radius=.039)
ring = bpy.context.object
ring.rotation_euler.x = math.pi/2
for face in ring.data.polygons:
    face.use_smooth = True
ring.data.materials.append(metal)
handle = rounded_box("Handle", (.12,.32,.13), .048, grip, (0,-.22,0))
loupe = merge("Magnifying glass", [ring, handle])

parts = [
    rounded_box("Cover", (.59,.49,.115), .042, cover),
    rounded_box("Page block", (.53,.42,.092), .026, page, (.021,.006,.046)),
    rounded_box("Binding", (.09,.46,.13), .032, cover, (-.242,0,.03)),
]
for j, width in enumerate((.32,.22,.28)):
    parts.append(rounded_box("Printed line", (width,.015,.013), .006, spine, (.02,.105-j*.09,.096)))
book = merge("Pocket notebook", parts)

bulb = ellipsoid("Bulb globe", glow, lambda x,y,z: (x*.177*(1+.25*y), y*.20+.075,z*.141), 24, 14, False)
socket = rounded_box("Socket", (.15,.12,.132), .03, metal, (0,-.145,0))
bulb = merge("Idea bulb", [bulb,socket])


def tube(name, points, radius, surface, sides=12):
    """A smooth, closed tube for small handles, bands and seams."""
    vertices, faces = [], []
    points = [Vector(point) for point in points]
    for i, point in enumerate(points):
        tangent = (points[min(i+1, len(points)-1)]-points[max(0, i-1)]).normalized()
        reference = Vector((0, 0, 1))
        if abs(tangent.dot(reference)) > .95:
            reference = Vector((0, 1, 0))
        side = tangent.cross(reference).normalized()
        up = tangent.cross(side).normalized()
        for j in range(sides):
            angle = math.tau*j/sides
            vertices.append(tuple(point+radius*(math.cos(angle)*side+math.sin(angle)*up)))
    for i in range(len(points)-1):
        for j in range(sides):
            a, b = i*sides+j, i*sides+(j+1) % sides
            faces.append((a, b, b+sides, a+sides))
    faces += [tuple(reversed(range(sides))), tuple((len(points)-1)*sides+j for j in range(sides))]
    return mesh_object(name, surface, vertices, faces, False)


cup_parts = [rounded_box("Porcelain cup", (.40,.38,.34), .09, page)]
cup_parts.append(tube("Cup handle", [(.20+.13*math.sin(math.pi*t/16), .12-.24*t/16, 0)
                                    for t in range(17)], .035, page))
cup_parts.append(ellipsoid("Tea surface", grip, lambda x,y,z: (x*.153,.189+y*.012,z*.122), 32, 12, False))
cup_parts.append(ellipsoid("Cup heart stamp", glow, lambda x,y,z: (x*.046,-.01+y*.046,.171+z*.01), 20, 12, False))
for i in range(2):
    cup_parts.append(tube("Quiet steam", [(-.06+i*.13+.018*math.sin(t*.7), .26+t*.025, 0)
                                         for t in range(8)], .008, spine, 8))
tea = merge("Porcelain tea cup", cup_parts)

pencil_parts = [rounded_box("Pencil barrel", (.078,.59,.078), .018, glow),
                rounded_box("Eraser", (.087,.10,.087), .025, grip, (0,.335,0)),
                rounded_box("Ferrule", (.085,.057,.085), .015, metal, (0,.27,0))]
pencil_parts.append(mesh_object("Sharpened tip", page,
    [(-.039,-.295,-.039),(.039,-.295,-.039),(.039,-.295,.039),(-.039,-.295,.039),(0,-.46,0)],
    [(0,1,4),(1,2,4),(2,3,4),(3,0,4),(3,2,1,0)], False))
pencil_parts.append(ellipsoid("Graphite point", eye, lambda x,y,z: (x*.019,-.445+y*.035,z*.019), 16, 12, False))
pencil = merge("Sketch pencil", pencil_parts)

headphone_parts = [tube("Headphone band", [(1.01*math.cos(t*math.pi/40), .05+.99*math.sin(t*math.pi/40), -.03)
                                           for t in range(41)], .049, cover)]
for side in (-1, 1):
    headphone_parts.append(rounded_box("Soft ear cushion", (.18,.40,.29), .075, grip, (side*.985,.07,0)))
    headphone_parts.append(rounded_box("Enamel ear cup", (.15,.32,.30), .065, cover, (side*1.075,.07,0)))
    headphone_parts.append(rounded_box("Brass ear cap", (.025,.17,.18), .012, metal, (side*1.15,.07,0)))
music = merge("Over-ear headphones", headphone_parts)

envelope_parts = [rounded_box("Envelope", (.62,.41,.05), .035, page)]
for direction in (-1, 1):
    envelope_parts.append(tube("Envelope fold", [(direction*.27*(1-t/12), .16-.18*t/12, .032)
                                                for t in range(13)], .009, spine, 8))
envelope_parts.append(ellipsoid("Wax seal", grip, lambda x,y,z: (x*.056,-.025+y*.056,.042+z*.014), 24, 16, False))
letter = merge("Little letter", envelope_parts)

telescope_parts = [tube("Telescope barrel", [(-.26,-.10,0),(.25,.16,0)], .105, metal, 24),
                   tube("Lens rim", [(.21,.14,0),(.29,.18,0)], .127, cover, 28),
                   tube("Eyepiece", [(-.34,-.14,0),(-.25,-.095,0)], .073, grip, 24),
                   ellipsoid("Telescope lens", spine, lambda x,y,z: (.29+x*.035,.18+y*.073,z*.100), 24, 16, False)]
telescope = merge("Pocket telescope", telescope_parts)

# A beat contains time, yaw/pitch/roll, lift, squash, blink, gaze,
# the prop position in three dimensions, its scale, and its tilt.
def beat(t, yaw=0, pitch=0, roll=0, lift=0, squash=0, blink=0, gaze=(0,0),
         position=(0,0,0), prop=0, twist=0):
    return (t,yaw,pitch,roll,lift,squash,blink,*gaze,*position,prop,twist)


clips = {
    "Look around": [beat(0),beat(.33,-.46,.03,.10,gaze=(-.04,.025)),
                    beat(.79,-.54,-.11,.13,gaze=(-.06,.05)),beat(1.09,-.06,.06,0,blink=.95),
                    beat(1.26,.37,-.07,-.11,gaze=(.05,.02)),
                    beat(1.89,.48,.12,-.08,gaze=(.04,-.03)),beat(2.52)],
    "Hop": [beat(0),beat(.21,-.09,.10,.10,-.08,.13),beat(.48,.06,-.11,-.16,.29,-.085),
            beat(.73,.08,.12,.11,.10),beat(.95,.09,.02,-.07,-.073,.095,blink=.7),
            beat(1.20,.06,-.08,.05,.069,-.045),beat(1.60)],
    "Think": [beat(0),beat(.40,.17,.08,-.16,gaze=(.03,.03)),
              beat(.79,-.13,-.24,-.21,.032,.025,.33,gaze=(.04,.08)),
              beat(1.35,-.17,-.31,-.16,.021,.042,.11,gaze=(.07,.085)),
              beat(1.85,-.28,-.30,-.13,.017,.027,.28,gaze=(.035,.106)),
              beat(2.38,.06,-.08,-.08,blink=.9,gaze=(-.035,.021)),beat(2.92)],
    "Investigate": [
        beat(0),beat(.29,.26,.21,.13,-.076,.086,gaze=(.05,-.04)),
        beat(.62,.14,.16,.06,position=(.82,-.19,.15),prop=.48,twist=-.58,gaze=(.043,-.06)),
        beat(.98,-.11,.08,-.07,.035,position=(.99,.21,.42),prop=.94,twist=-.34,gaze=(.078,.021)),
        beat(1.55,.13,-.03,-.12,.025,position=(.70,.43,.85),prop=1,twist=.21,gaze=(.055,.046)),
        beat(2.10,.24,.11,-.19,.03,position=(.61,.35,1.0),prop=1,twist=-.23,gaze=(.03,.025)),
        beat(2.74,-.11,.23,-.04,position=(.92,-.04,.55),prop=.94,twist=-.35,gaze=(.074,-.04)),
        beat(3.21,.10,.10,.08,position=(.83,-.14,.14),prop=.32,twist=-.57),beat(3.55)],
    "Notebook": [
        beat(0),beat(.33,-.10,.12,-.12,-.07,.086,gaze=(.04,-.06)),
        beat(.68,.03,.15,.06,position=(.84,-.35,.28),prop=.54,twist=.34,gaze=(.047,-.07)),
        beat(1.12,-.15,.21,-.07,position=(.51,-.42,.94),prop=1.23,twist=-.10,gaze=(.026,-.083)),
        beat(1.67,.13,.26,.045,position=(.38,-.38,1.0),prop=1.26,twist=.12,gaze=(-.045,-.081)),
        beat(2.27,-.11,.21,-.055,position=(.51,-.46,1.0),prop=1.22,twist=-.08,gaze=(.035,-.09)),
        beat(2.73,.02,.19,.03,position=(.50,-.47,.88),prop=1.16,twist=.09,gaze=(.044,-.07)),
        beat(3.14,.08,.12,.04,position=(.84,-.49,.28),prop=.62,twist=.39),beat(3.60)],
    "Idea": [
        beat(0),beat(.28,.14,.17,-.13,-.08,.09,blink=.55),
        beat(.60,-.21,-.26,.14,.12,-.08,position=(.71,.72,.13),prop=.7,twist=-.34,gaze=(.04,.07)),
        beat(.90,-.13,-.16,.13,.045,-.02,position=(.78,.92,.24),prop=1,twist=.14,gaze=(.05,.083)),
        beat(1.45,.07,-.20,.035,position=(.76,.9,.24),prop=1,twist=-.13,gaze=(.05,.073)),
        beat(1.98,.07,.08,position=(.72,.69,.18),prop=.25,twist=.24),beat(2.28)],
    "Nod": [beat(0),beat(.34,-.13,-.09,.13,.05),beat(.60,-.10,.22,.10,-.035,.04,blink=.29),
            beat(.95,-.06,-.08,.10,.035),beat(1.42)],
    "Talk": [beat(0),beat(.24,.10,.12,-.12,-.037,.03,gaze=(.04,-.012)),
             beat(.59,-.17,-.06,.12,.07,-.04,gaze=(-.03,.034)),
             beat(.89,.06,.15,-.10,-.02,.035,blink=.42),
             beat(1.24,.17,-.05,-.12,.055,gaze=(.04,.018)),beat(1.65)],
    "Twirl": [beat(0),beat(.21,-.19,.10,.10,-.07,.10),
              beat(.43,.40,-.14,-.16,.20,-.065),
              beat(.93,5.72,.10,.15,.10,-.02),
              beat(1.18,math.tau,.13,-.095,-.07,.09,blink=.62),
              beat(1.45,math.tau,-.05,.045,.065,-.02),beat(1.72,math.tau)],
    "Double hop": [beat(0),beat(.18,-.12,.09,-.14,-.073,.10),
                   beat(.43,.15,-.16,.18,.22,-.065),beat(.66,.08,.11,-.07,-.01,.09),
                   beat(.91,-.21,-.10,-.21,.29,-.07),beat(1.18,.05,.15,.12,-.049,.11,blink=.7),
                   beat(1.42,.12,.02,-.043,.068,-.027),beat(1.70)],
    "Curious tilt": [beat(0),beat(.34,.28,.22,-.25,.015,.025,gaze=(.045,-.02)),
                     beat(.82,.37,.27,-.34,.031,.024,.30,gaze=(.06,-.022)),
                     beat(1.14,.30,.13,-.24,gaze=(-.025,.056)),
                     beat(1.46,-.22,-.12,.14,.064,blink=.72),beat(1.88)],
}

# Longer holds and smaller settling motions keep familiar gestures relaxed.
for name, keys in clips.items():
    relaxed = []
    for key in keys:
        values = list(key)
        values[0] *= 1.4 if name != "Twirl" else 1.6
        if name != "Twirl":
            for index in (1, 2, 3, 4, 5):
                values[index] *= .72
        relaxed.append(tuple(values))
    clips[name] = relaxed

clips.update({
    "Tea": [beat(0), beat(.8, .08, .10, -.04, position=(.77,-.39,.32), prop=.65),
            beat(1.6, -.08, .07, -.06, position=(.51,-.23,.91), prop=1, twist=-.16, gaze=(.04,-.045)),
            beat(2.9, -.08, .10, -.04, blink=.30, position=(.45,-.13,1.02), prop=1, twist=-.30),
            beat(4.1, .02, .08, .02, blink=.18, position=(.48,-.24,.94), prop=1, twist=-.08),
            beat(5.0, .04, .04, .02, position=(.74,-.40,.42), prop=.66), beat(5.9)],
    "Sketch": [beat(0), beat(.9, -.07, .09, -.05, position=(.79,-.17,.60), prop=.6, twist=-.45),
               beat(1.6, -.08, .16, -.03, position=(.72,-.21,.91), prop=1, twist=-.55, gaze=(.03,-.055)),
               beat(2.4, .02, .17, .02, position=(.82,-.19,.96), prop=1, twist=-.30, gaze=(.05,-.05)),
               beat(3.3, -.05, .13, -.03, position=(.70,-.24,.96), prop=1, twist=-.63),
               beat(4.2, .02, .12, .02, position=(.81,-.18,.94), prop=1, twist=-.34),
               beat(5.0, -.03, -.08, -.03, position=(.84,-.13,.78), prop=1, twist=-.12, gaze=(.02,.05)),
               beat(5.8, .03, .04, .02, position=(.80,-.32,.40), prop=.50), beat(6.7)],
    "Music": [beat(0), beat(1.0, position=(0,.02,0), prop=.80),
              beat(1.7, roll=-.045, position=(0,.02,0), prop=1, blink=.20),
              beat(2.9, roll=.055, position=(0,.02,0), prop=1, blink=.35),
              beat(4.3, roll=-.055, position=(0,.02,0), prop=1, blink=.30),
              beat(5.5, roll=.025, position=(0,.02,0), prop=1, blink=.20),
              beat(6.4, position=(0,.10,-.05), prop=.72), beat(7.3)],
    "Letter": [beat(0), beat(.8, -.06, .09, -.03, position=(.71,-.37,.40), prop=.65, twist=.15),
               beat(1.6, -.07, .16, -.04, position=(.46,-.33,.99), prop=1.12, twist=-.05, gaze=(.015,-.06)),
               beat(3.1, .07, .15, .035, position=(.47,-.30,.99), prop=1.12, twist=.04, gaze=(-.025,-.055)),
               beat(4.5, -.02, .12, -.01, position=(.47,-.34,.97), prop=1.10, twist=-.03, blink=.8),
               beat(5.4, .02, .04, .02, position=(.74,-.41,.40), prop=.50), beat(6.2)],
    "Telescope": [beat(0), beat(.9, .13, -.08, -.03, position=(.70,.20,.30), prop=.55),
                  beat(1.7, .16, -.13, -.05, position=(.51,.23,.96), prop=1, twist=.03, gaze=(.04,.045)),
                  beat(3.0, .24, -.18, -.06, position=(.55,.29,1.02), prop=1, twist=.13, gaze=(.06,.065)),
                  beat(4.4, .06, -.09, -.03, position=(.49,.17,.99), prop=1, twist=-.06),
                  beat(5.3, -.02, .07, .03, position=(.78,-.11,.40), prop=.5), beat(6.2)],
    "Sway": [beat(0), beat(1.2, roll=.065, lift=.009), beat(2.7, roll=-.065, lift=.014),
             beat(4.1, roll=.035, lift=.006), beat(5.2)],
    "Stretch": [beat(0), beat(.9, pitch=.07, squash=.035, blink=.25),
                beat(2.0, pitch=-.10, lift=.035, squash=-.045, blink=.20, gaze=(0,.025)),
                beat(3.2, pitch=-.07, lift=.025, squash=-.025), beat(4.7)],
    "Sleepy": [beat(0), beat(1.2, pitch=.10, roll=.05, blink=.35),
               beat(2.5, pitch=.14, roll=.055, blink=.75, squash=.014),
               beat(3.8, pitch=.13, roll=.055, blink=.85, squash=.012),
               beat(4.8, pitch=-.035, roll=-.025, blink=.30, gaze=(.018,.02)), beat(6.2)],
    "Peek": [beat(0), beat(.9, yaw=-.22, roll=.10, gaze=(-.045,.015)),
             beat(2.0, yaw=-.28, roll=.12, gaze=(-.06,.025)),
             beat(3.2, yaw=-.18, roll=.075, blink=.8), beat(4.6)],
    "Look up": [beat(0), beat(1.0, yaw=.08, pitch=-.14, roll=-.04, gaze=(.025,.055)),
                beat(2.6, yaw=-.07, pitch=-.17, roll=.03, gaze=(-.025,.065)),
                beat(3.8, yaw=-.03, pitch=-.09, gaze=(-.015,.035)), beat(5.1)],
    "Settle": [beat(0), beat(1.0, lift=-.02, squash=.026, blink=.55),
               beat(2.4, lift=-.025, squash=.032, blink=.25),
               beat(3.7, pitch=-.025, lift=.005, squash=-.01), beat(4.8)],
})

# Editable transform rig with labeled takes on the Blender timeline.
root = empty("Bud rig")
root.rotation_mode = "YXZ"
bodies["orb"].parent = root
tuft.parent = root
set_transform(tuft, (-.23,.79,.08), -20)
eye_nodes = [eyes, eyes.copy()]
bpy.context.collection.objects.link(eye_nodes[1])
for i, obj in enumerate(eye_nodes):
    obj.name = ("Left" if i == 0 else "Right") + " eye"
    obj.parent = root
props = {"Investigate": loupe, "Notebook": book, "Idea": bulb, "Tea": tea,
         "Sketch": pencil, "Music": music, "Letter": letter, "Telescope": telescope}
for obj in props.values():
    obj.parent = root

scene = bpy.context.scene
scene.render.fps = 30
frame = 1
for name, beats in clips.items():
    scene.timeline_markers.new(name, frame=frame)
    for t,yaw,pitch,roll,lift,squash,blink,gx,gy,px,py,pz,pop,twist in beats:
        root.rotation_euler = (.025 + pitch, -.12 - roll, -.145 - yaw)
        root.location.z = lift+.045
        root.scale = (1+squash,1+squash*.6,1-squash)
        for i, obj in enumerate(eye_nodes):
            x,y,z = anchors["orb"][i]
            set_transform(obj, (x+gx,y+gy,z - .30*gy), (22,18)[i], ((1,.90)[i],1-.86*blink,1))
            for key in ("location","scale"):
                obj.keyframe_insert(key, frame=frame+t*30)
        for action, prop in props.items():
            size = max(.001, pop) if name == action else .001
            set_transform(prop,(px,py,pz),math.degrees(twist),(size,size,size))
            for key in ("location","rotation_euler","scale"):
                prop.keyframe_insert(key, frame=frame+t*30)
        for key in ("location","rotation_euler","scale"):
            root.keyframe_insert(key, frame=frame+t*30)
    frame += math.ceil(beats[-1][0]*30)+12
scene.frame_end = frame
scene.frame_set(1)

def point_at(obj, target):
    obj.rotation_euler = (Vector(target)-obj.location).to_track_quat("-Z","Y").to_euler()

bpy.ops.object.camera_add(location=(3,-8,2.6))
camera = bpy.context.object
point_at(camera,(0,0,.1))
camera.data.type = "ORTHO"
camera.data.ortho_scale = 3.55
scene.camera = camera
for name, location, energy, size in (
    ("Softbox",(-3,-4,6),485,5),("Bounce",(4,-3,2),160,4),("Edge light",(1,3,5),280,3),
):
    bpy.ops.object.light_add(type="AREA",location=location)
    light=bpy.context.object
    light.name=name
    light.data.energy, light.data.shape, light.data.size=energy,"DISK",size
    point_at(light,(0,0,0))
scene.world = bpy.data.worlds.new("Studio")
scene.world.color=(.19,.19,.19)
scene.render.engine="BLENDER_EEVEE"
scene.render.film_transparent=True
scene.render.resolution_x=scene.render.resolution_y=512
scene.render.resolution_percentage=100
for area in bpy.context.screen.areas:
    if area.type == "VIEW_3D":
        area.spaces.active.region_3d.view_perspective = "CAMERA"
        area.spaces.active.shading.color_type = "MATERIAL"
bpy.ops.object.select_all(action="DESELECT")
root.select_set(True)
bpy.context.view_layer.objects.active=root
bpy.context.preferences.filepaths.save_version=0
bpy.ops.wm.save_as_mainfile(filepath=str(ROOT/"assets/character/Bud.blend"))

materials=(body,eye,page,cover,metal,grip,glow,spine)
with (ROOT/"internal/budmodel/Bud.mesh").open("wb") as out:
    out.write(b"BUD5")
    out.write(struct.pack("<3B",len(SHAPES),len(meshes),len(clips)+1))
    for shape in SHAPES:
        for anchor in anchors[shape]:
            out.write(struct.pack("<3f",*anchor))
    for obj in meshes:
        mesh=obj.data
        mesh.calc_loop_triangles()
        out.write(struct.pack("<2H",len(mesh.vertices),len(mesh.loop_triangles)))
        for vertex in mesh.vertices:
            co, normal = vertex.co,vertex.normal
            out.write(struct.pack("<6h",*(round(v*12000) for v in (co.x,co.z,-co.y)),
                                  *(round(v*32767) for v in (normal.x,normal.z,-normal.y))))
        for tri in mesh.loop_triangles:
            index=materials.index(obj.material_slots[tri.material_index].material)
            out.write(struct.pack("<3HB",*tri.vertices,index))
    for keys in clips.values():
        out.write(struct.pack("<B",len(keys)))
        for key in keys:
            out.write(struct.pack("<14f",*key))
