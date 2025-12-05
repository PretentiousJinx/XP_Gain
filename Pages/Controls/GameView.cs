using XP_Gain.Game;
using Microsoft.Maui.Controls;
using XP_Gain.Game;

namespace XP_Gain.Pages.Controls
{
    public class GameView : ContentView
    {
        private Game1 _game;

        public GameView()
        {
            Loaded += (s, e) =>
            {
                if (_game == null)
                {
                    _game = new Game1();
                    // Run MonoGame
                    _game.Run();
                }
            };
        }
    }
}
